package cosmos

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	clienttypes "github.com/cosmos/ibc-go/v3/modules/core/02-client/types"
	conntypes "github.com/cosmos/ibc-go/v3/modules/core/03-connection/types"
	chantypes "github.com/cosmos/ibc-go/v3/modules/core/04-channel/types"
	"github.com/cosmos/relayer/v2/relayer/processor"
	"github.com/cosmos/relayer/v2/relayer/provider"
	abci "github.com/tendermint/tendermint/abci/types"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ibcMessage is the type used for parsing all possible properties of IBC messages
type ibcMessage struct {
	eventType string
	info      ibcMessageInfo
}

type ibcMessageInfo interface {
	parseAttrs(log *zap.Logger, attrs []sdk.Attribute)
	MarshalLogObject(enc zapcore.ObjectEncoder) error
}

func (ccp *CosmosChainProcessor) ibcMessagesFromBlockEvents(
	beginBlockEvents, endBlockEvents []abci.Event,
	height uint64,
) (res []ibcMessage) {
	chainID := ccp.chainProvider.ChainId()
	beginBlockStringified := sdk.StringifyEvents(beginBlockEvents)
	for _, event := range beginBlockStringified {
		switch event.Type {
		case chantypes.EventTypeSendPacket:
			sendPacketMessages := parseIBCSendPacketMessagesFromEvent(ccp.log, event, chainID, height)
			res = append(res, sendPacketMessages...)
		case chantypes.EventTypeChannelOpenInit:
			openInitMsgs := parseIBCChannelOpenInitMessagesFromEvent(ccp.log, event, chainID, height)
			res = append(res, openInitMsgs...)
		default:
			m := parseIBCMessageFromEvent(ccp.log, event, chainID, height)
			if m != nil && m.info != nil {
				res = append(res, *m)
			}
			// Not an IBC message, don't need to log here
		}
	}

	endBlockStringified := sdk.StringifyEvents(endBlockEvents)
	for _, event := range endBlockStringified {
		switch event.Type {
		case chantypes.EventTypeSendPacket:
			sendPacketMessages := parseIBCSendPacketMessagesFromEvent(ccp.log, event, chainID, height)
			res = append(res, sendPacketMessages...)
		case chantypes.EventTypeChannelOpenInit:
			openInitMsgs := parseIBCChannelOpenInitMessagesFromEvent(ccp.log, event, chainID, height)
			res = append(res, openInitMsgs...)
		default:
			m := parseIBCMessageFromEvent(ccp.log, event, chainID, height)
			if m != nil && m.info != nil {
				res = append(res, *m)
			}
			// Not an IBC message, don't need to log here
		}
	}
	return res
}

// ibcMessagesFromTransaction parses all events within a transaction to find IBC messages
func (ccp *CosmosChainProcessor) ibcMessagesFromTransaction(tx *abci.ResponseDeliverTx, height uint64) []ibcMessage {
	parsedLogs, err := sdk.ParseABCILogs(tx.Log)
	if err != nil {
		ccp.log.Info("Failed to parse abci logs", zap.Error(err))
		return nil
	}
	return parseABCILogs(ccp.log, parsedLogs, ccp.chainProvider.ChainId(), height)
}

func parseABCILogs(log *zap.Logger, logs sdk.ABCIMessageLogs, chainID string, height uint64) (messages []ibcMessage) {
	for _, msgLog := range logs {
		recvPacketMsg := new(ibcMessage)
		for _, event := range msgLog.Events {
			switch event.Type {
			case chantypes.EventTypeRecvPacket, chantypes.EventTypeWriteAck:
				recvPacketMsg.parseIBCPacketReceiveMessageFromEvent(log, event, chainID, height)
			case chantypes.EventTypeSendPacket:
				sendPacketMessages := parseIBCSendPacketMessagesFromEvent(log, event, chainID, height)
				messages = append(messages, sendPacketMessages...)
			case chantypes.EventTypeChannelOpenInit:
				openInitMsgs := parseIBCChannelOpenInitMessagesFromEvent(log, event, chainID, height)
				messages = append(messages, openInitMsgs...)
			default:
				m := parseIBCMessageFromEvent(log, event, chainID, height)
				if m == nil || m.info == nil {
					// Not an IBC message, don't need to log here
					continue
				}
				messages = append(messages, *m)
			}
		}

		if recvPacketMsg != nil && recvPacketMsg.info != nil {
			// Not a packet IBC message, don't need to log here
			messages = append(messages, *recvPacketMsg)
		}
	}

	return messages
}

func parseIBCChannelOpenInitMessagesFromEvent(
	log *zap.Logger,
	event sdk.StringEvent,
	chainID string,
	height uint64,
) (out []ibcMessage) {

	cis := parseChannelAttrsMultiple(log, height, event.Attributes)
	for _, ci := range cis {
		out = append(out, ibcMessage{eventType: event.Type, info: ci})
	}

	return out
}

func parseIBCSendPacketMessagesFromEvent(
	log *zap.Logger,
	event sdk.StringEvent,
	chainID string,
	height uint64,
) (out []ibcMessage) {

	pis := parsePacketAttrsMultiple(log, height, event.Attributes)
	for _, pi := range pis {
		out = append(out, ibcMessage{eventType: event.Type, info: pi})
	}

	return out
}

func parseIBCMessageFromEvent(
	log *zap.Logger,
	event sdk.StringEvent,
	chainID string,
	height uint64,
) *ibcMessage {
	switch event.Type {
	case chantypes.EventTypeSendPacket,
		chantypes.EventTypeAcknowledgePacket, chantypes.EventTypeTimeoutPacket,
		chantypes.EventTypeTimeoutPacketOnClose:
		pi := &packetInfo{Height: height}
		pi.parseAttrs(log, event.Attributes)
		return &ibcMessage{
			eventType: event.Type,
			info:      pi,
		}
	case chantypes.EventTypeChannelOpenTry,
		chantypes.EventTypeChannelOpenAck, chantypes.EventTypeChannelOpenConfirm,
		chantypes.EventTypeChannelCloseInit, chantypes.EventTypeChannelCloseConfirm:
		ci := &channelInfo{Height: height}
		ci.parseAttrs(log, event.Attributes)
		return &ibcMessage{
			eventType: event.Type,
			info:      ci,
		}
	case conntypes.EventTypeConnectionOpenInit, conntypes.EventTypeConnectionOpenTry,
		conntypes.EventTypeConnectionOpenAck, conntypes.EventTypeConnectionOpenConfirm:
		ci := &connectionInfo{Height: height}
		ci.parseAttrs(log, event.Attributes)
		return &ibcMessage{
			eventType: event.Type,
			info:      ci,
		}
	case clienttypes.EventTypeCreateClient, clienttypes.EventTypeUpdateClient,
		clienttypes.EventTypeUpgradeClient, clienttypes.EventTypeSubmitMisbehaviour,
		clienttypes.EventTypeUpdateClientProposal:
		ci := new(clientInfo)
		ci.parseAttrs(log, event.Attributes)
		return &ibcMessage{
			eventType: event.Type,
			info:      ci,
		}
	case processor.ClientICQTypeRequest, processor.ClientICQTypeResponse:
		ci := &clientICQInfo{
			Height: height,
			Source: chainID,
		}
		ci.parseAttrs(log, event.Attributes)
		return &ibcMessage{
			eventType: event.Type,
			info:      ci,
		}
	}
	return nil
}

func (msg *ibcMessage) parseIBCPacketReceiveMessageFromEvent(
	log *zap.Logger,
	event sdk.StringEvent,
	chainID string,
	height uint64,
) *ibcMessage {
	var pi *packetInfo
	if msg.info == nil {
		pi = &packetInfo{Height: height}
		msg.info = pi
	} else {
		pi = msg.info.(*packetInfo)
	}
	pi.parseAttrs(log, event.Attributes)
	if event.Type != chantypes.EventTypeWriteAck {
		msg.eventType = event.Type
	}
	return msg
}

// clientInfo contains the consensus height of the counterparty chain for a client.
type clientInfo struct {
	clientID        string
	consensusHeight clienttypes.Height
	header          []byte
}

func (c clientInfo) ClientState() provider.ClientState {
	return provider.ClientState{
		ClientID:        c.clientID,
		ConsensusHeight: c.consensusHeight,
	}
}

func (res *clientInfo) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddString("client_id", res.clientID)
	enc.AddUint64("consensus_height", res.consensusHeight.RevisionHeight)
	enc.AddUint64("consensus_height_revision", res.consensusHeight.RevisionNumber)
	return nil
}

func (res *clientInfo) parseAttrs(log *zap.Logger, attributes []sdk.Attribute) {
	for _, attr := range attributes {
		res.parseClientAttribute(log, attr)
	}
}

func (res *clientInfo) parseClientAttribute(log *zap.Logger, attr sdk.Attribute) {
	switch attr.Key {
	case clienttypes.AttributeKeyClientID:
		res.clientID = attr.Value
	case clienttypes.AttributeKeyConsensusHeight:
		revisionSplit := strings.Split(attr.Value, "-")
		if len(revisionSplit) != 2 {
			log.Error("Error parsing client consensus height",
				zap.String("client_id", res.clientID),
				zap.String("value", attr.Value),
			)
			return
		}
		revisionNumberString := revisionSplit[0]
		revisionNumber, err := strconv.ParseUint(revisionNumberString, 10, 64)
		if err != nil {
			log.Error("Error parsing client consensus height revision number",
				zap.Error(err),
			)
			return
		}
		revisionHeightString := revisionSplit[1]
		revisionHeight, err := strconv.ParseUint(revisionHeightString, 10, 64)
		if err != nil {
			log.Error("Error parsing client consensus height revision height",
				zap.Error(err),
			)
			return
		}
		res.consensusHeight = clienttypes.Height{
			RevisionNumber: revisionNumber,
			RevisionHeight: revisionHeight,
		}
	case clienttypes.AttributeKeyHeader:
		data, err := hex.DecodeString(attr.Value)
		if err != nil {
			log.Error("Error parsing client header",
				zap.String("header", attr.Value),
				zap.Error(err),
			)
			return
		}
		res.header = data
	}
}

// alias type to the provider types, used for adding parser methods
type packetInfo provider.PacketInfo

func (res *packetInfo) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddUint64("sequence", res.Sequence)
	enc.AddString("src_channel", res.SourceChannel)
	enc.AddString("src_port", res.SourcePort)
	enc.AddString("dst_channel", res.DestChannel)
	enc.AddString("dst_port", res.DestPort)
	return nil
}

// parsePacketInfo is treated differently from the others since it can be constructed from the accumulation of multiple events
func (res *packetInfo) parseAttrs(log *zap.Logger, attrs []sdk.Attribute) {
	for _, attr := range attrs {
		res.parsePacketAttribute(log, attr)
	}
}

// parseChannelAttrsMultiple parsed a single event into potentially multiple packetInfo.
// This is needed for ICA send packet messages.
func parsePacketAttrsMultiple(log *zap.Logger, height uint64, attrs []sdk.Attribute) (out []*packetInfo) {
	pi := &packetInfo{Height: height}
	for _, attr := range attrs {
		if pi.parsePacketAttribute(log, attr) {
			// this attribute has already been parsed for this channelInfo,
			// so save this channelInfo and start a new one.
			out = append(out, pi)
			pi = &packetInfo{Height: height}
			// re-parse attribute now that we have fresh channelInfo.
			pi.parsePacketAttribute(log, attr)
		}
	}
	if pi.isFullyParsed() {
		out = append(out, pi)
	}

	return out
}

func (res *packetInfo) isFullyParsed() bool {
	return res.Sequence != 0 && res.TimeoutTimestamp != 0 &&
		res.TimeoutHeight != clienttypes.NewHeight(0, 0) &&
		res.ChannelOrder != "" && len(res.Data) > 0 &&
		res.SourceChannel != "" && res.SourcePort != "" &&
		res.DestChannel != "" && res.DestPort != ""
}

func (res *packetInfo) parsePacketAttribute(log *zap.Logger, attr sdk.Attribute) bool {
	var err error
	switch attr.Key {
	case chantypes.AttributeKeySequence:
		if res.Sequence != 0 {
			return true
		}
		res.Sequence, err = strconv.ParseUint(attr.Value, 10, 64)
		if err != nil {
			log.Error("Error parsing packet sequence",
				zap.String("value", attr.Value),
				zap.Error(err),
			)
			return false
		}
	case chantypes.AttributeKeyTimeoutTimestamp:
		if res.TimeoutTimestamp != 0 {
			return true
		}
		res.TimeoutTimestamp, err = strconv.ParseUint(attr.Value, 10, 64)
		if err != nil {
			log.Error("Error parsing packet timestamp",
				zap.Uint64("sequence", res.Sequence),
				zap.String("value", attr.Value),
				zap.Error(err),
			)
			return false
		}
	// NOTE: deprecated per IBC spec
	case chantypes.AttributeKeyData:
		res.Data = []byte(attr.Value)
	case chantypes.AttributeKeyDataHex:
		if len(res.Data) > 0 {
			return true
		}
		data, err := hex.DecodeString(attr.Value)
		if err != nil {
			log.Error("Error parsing packet data",
				zap.Uint64("sequence", res.Sequence),
				zap.Error(err),
			)
			return false
		}
		res.Data = data
	// NOTE: deprecated per IBC spec
	case chantypes.AttributeKeyAck:
		res.Ack = []byte(attr.Value)
	case chantypes.AttributeKeyAckHex:
		if len(res.Ack) > 0 {
			return true
		}
		data, err := hex.DecodeString(attr.Value)
		if err != nil {
			log.Error("Error parsing packet ack",
				zap.Uint64("sequence", res.Sequence),
				zap.String("value", attr.Value),
				zap.Error(err),
			)
			return false
		}
		res.Ack = data
	case chantypes.AttributeKeyTimeoutHeight:
		if res.TimeoutHeight != clienttypes.NewHeight(0, 0) {
			return true
		}
		timeoutSplit := strings.Split(attr.Value, "-")
		if len(timeoutSplit) != 2 {
			log.Error("Error parsing packet height timeout",
				zap.Uint64("sequence", res.Sequence),
				zap.String("value", attr.Value),
			)
			return false
		}
		revisionNumber, err := strconv.ParseUint(timeoutSplit[0], 10, 64)
		if err != nil {
			log.Error("Error parsing packet timeout height revision number",
				zap.Uint64("sequence", res.Sequence),
				zap.String("value", timeoutSplit[0]),
				zap.Error(err),
			)
			return false
		}
		revisionHeight, err := strconv.ParseUint(timeoutSplit[1], 10, 64)
		if err != nil {
			log.Error("Error parsing packet timeout height revision height",
				zap.Uint64("sequence", res.Sequence),
				zap.String("value", timeoutSplit[1]),
				zap.Error(err),
			)
			return false
		}
		res.TimeoutHeight = clienttypes.Height{
			RevisionNumber: revisionNumber,
			RevisionHeight: revisionHeight,
		}
	case chantypes.AttributeKeySrcPort:
		if res.SourcePort != "" {
			return true
		}
		res.SourcePort = attr.Value
	case chantypes.AttributeKeySrcChannel:
		if res.SourceChannel != "" {
			return true
		}
		res.SourceChannel = attr.Value
	case chantypes.AttributeKeyDstPort:
		if res.DestPort != "" {
			return true
		}
		res.DestPort = attr.Value
	case chantypes.AttributeKeyDstChannel:
		if res.DestChannel != "" {
			return true
		}
		res.DestChannel = attr.Value
	case chantypes.AttributeKeyChannelOrdering:
		if res.ChannelOrder != "" {
			return true
		}
		res.ChannelOrder = attr.Value
	}
	return false
}

type clientICQInfo struct {
	Source     string
	Connection string
	Chain      string
	QueryID    provider.ClientICQQueryID
	Type       string
	Request    []byte
	Height     uint64
}

func (res *clientICQInfo) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddString("connection_id", res.Connection)
	enc.AddString("chain_id", res.Chain)
	enc.AddString("query_id", string(res.QueryID))
	enc.AddString("type", res.Type)
	enc.AddString("request", hex.EncodeToString(res.Request))
	enc.AddUint64("height", res.Height)

	return nil
}

func (res *clientICQInfo) parseAttrs(log *zap.Logger, attrs []sdk.Attribute) {
	for _, attr := range attrs {
		if err := res.parseAttribute(attr); err != nil {
			panic(fmt.Errorf("failed to parse attributes from client ICQ message: %w", err))
		}
	}
}

func (res *clientICQInfo) parseAttribute(attr sdk.Attribute) (err error) {
	switch attr.Key {
	case "connection_id":
		res.Connection = attr.Value
	case "chain_id":
		res.Chain = attr.Value
	case "query_id":
		res.QueryID = provider.ClientICQQueryID(attr.Value)
	case "type":
		res.Type = attr.Value
	case "request":
		res.Request, err = hex.DecodeString(attr.Value)
		if err != nil {
			return err
		}
	case "height":
		res.Height, err = strconv.ParseUint(attr.Value, 10, 64)
		if err != nil {
			return err
		}
	}
	return nil
}

// alias type to the provider types, used for adding parser methods
type channelInfo provider.ChannelInfo

func (res *channelInfo) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddString("channel_id", res.ChannelID)
	enc.AddString("port_id", res.PortID)
	enc.AddString("counterparty_channel_id", res.CounterpartyChannelID)
	enc.AddString("counterparty_port_id", res.CounterpartyPortID)
	return nil
}

func (res *channelInfo) parseAttrs(log *zap.Logger, attrs []sdk.Attribute) {
	for _, attr := range attrs {
		res.parseChannelAttribute(attr)
	}
}

func (res *channelInfo) isFullyParsed() bool {
	return res.ChannelID != "" && res.PortID != "" &&
		res.CounterpartyPortID != "" && res.ConnID != ""
}

// parseChannelAttrsMultiple parsed a single event into potentially multiple channelInfo.
// This is needed for ICA channelOpenInit messages.
func parseChannelAttrsMultiple(log *zap.Logger, height uint64, attrs []sdk.Attribute) (out []*channelInfo) {
	ci := &channelInfo{Height: height}
	for _, attr := range attrs {
		if ci.parseChannelAttribute(attr) {
			// this attribute has already been parsed for this channelInfo,
			// so save this channelInfo and start a new one.
			out = append(out, ci)
			ci = &channelInfo{Height: height}
			// re-parse attribute now that we have fresh channelInfo.
			ci.parseChannelAttribute(attr)
		}
	}
	if ci.isFullyParsed() {
		out = append(out, ci)
	}

	return out
}

// parseChannelAttribute parses channel attributes from an event.
// If the attribute has already been parsed into the channelInfo,
// it will not overwrite, and return true to inform the caller that
// the attribute already exists.
func (res *channelInfo) parseChannelAttribute(attr sdk.Attribute) bool {
	switch attr.Key {
	case chantypes.AttributeKeyPortID:
		if res.PortID != "" {
			return true
		}
		res.PortID = attr.Value
	case chantypes.AttributeKeyChannelID:
		if res.ChannelID != "" {
			return true
		}
		res.ChannelID = attr.Value
	case chantypes.AttributeCounterpartyPortID:
		if res.CounterpartyPortID != "" {
			return true
		}
		res.CounterpartyPortID = attr.Value
	case chantypes.AttributeCounterpartyChannelID:
		if res.CounterpartyChannelID != "" {
			return true
		}
		res.CounterpartyChannelID = attr.Value
	case chantypes.AttributeKeyConnectionID:
		if res.ConnID != "" {
			return true
		}
		res.ConnID = attr.Value
	case chantypes.AttributeVersion:
		if res.Version != "" {
			return true
		}
		res.Version = attr.Value
	}
	return false
}

// alias type to the provider types, used for adding parser methods
type connectionInfo provider.ConnectionInfo

func (res *connectionInfo) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddString("connection_id", res.ConnID)
	enc.AddString("client_id", res.ClientID)
	enc.AddString("counterparty_connection_id", res.CounterpartyConnID)
	enc.AddString("counterparty_client_id", res.CounterpartyClientID)
	return nil
}

func (res *connectionInfo) parseAttrs(log *zap.Logger, attrs []sdk.Attribute) {
	for _, attr := range attrs {
		res.parseConnectionAttribute(attr)
	}
}

func (res *connectionInfo) parseConnectionAttribute(attr sdk.Attribute) {
	switch attr.Key {
	case conntypes.AttributeKeyConnectionID:
		res.ConnID = attr.Value
	case conntypes.AttributeKeyClientID:
		res.ClientID = attr.Value
	case conntypes.AttributeKeyCounterpartyConnectionID:
		res.CounterpartyConnID = attr.Value
	case conntypes.AttributeKeyCounterpartyClientID:
		res.CounterpartyClientID = attr.Value
	}
}
