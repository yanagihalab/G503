package cmd

import (
	cmtcfg "github.com/cometbft/cometbft/config"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
)

func initCometBFTConfig() *cmtcfg.Config {
	return cmtcfg.DefaultConfig()
}

func initAppConfig() (string, interface{}) {
	return serverconfig.DefaultConfigTemplate, serverconfig.DefaultConfig()
}
