build-docker:
	docker build --tag stride:dydx-interchain .

test:
	go test -timeout 15m -run ^TestScenarioStrideICAandICQ$$ github.com/cosmos/relayer/v2/interchaintest/stride -race -v -count=1