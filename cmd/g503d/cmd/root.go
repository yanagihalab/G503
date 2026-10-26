package cmd

import (
	"context"
	"os"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/config"
	"github.com/cosmos/cosmos-sdk/server"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkversion "github.com/cosmos/cosmos-sdk/version"
	"github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/spf13/cobra"

	"github.com/yanagihalab/G503/app"
)

func NewRootCmd() *cobra.Command {
	encoding := app.MakeEncodingConfig()
	clientCtx := client.Context{}.
		WithCodec(encoding.Codec).
		WithInterfaceRegistry(encoding.InterfaceRegistry).
		WithLegacyAmino(encoding.LegacyAmino).
		WithTxConfig(encoding.TxConfig).
		WithInput(os.Stdin).
		WithAccountRetriever(types.AccountRetriever{}).
		WithHomeDir(app.DefaultNodeHome).
		WithViper(app.Name)

	rootCmd := &cobra.Command{
		Use:           app.Name + "d",
		Short:         "G503 sanction chain node",
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Name() == "version" || cmd.Name() == "init" {
				return nil
			}
			cmd.SetOut(cmd.OutOrStdout())
			cmd.SetErr(cmd.ErrOrStderr())
			clientCtx = clientCtx.WithCmdContext(cmd.Context()).WithViper(app.Name)
			nextCtx, err := client.ReadPersistentCommandFlags(clientCtx, cmd.Flags())
			if err != nil {
				return err
			}
			nextCtx, err = config.ReadFromClientConfig(nextCtx)
			if err != nil {
				return err
			}
			if err := client.SetCmdClientContextHandler(nextCtx, cmd); err != nil {
				return err
			}
			customAppTemplate, customAppConfig := initAppConfig()
			return server.InterceptConfigsPreRunHandler(cmd, customAppTemplate, customAppConfig, initCometBFTConfig())
		},
	}
	serverCtx := server.NewDefaultContext()
	cmdCtx := context.WithValue(context.Background(), server.ServerContextKey, serverCtx)
	cmdCtx = context.WithValue(cmdCtx, client.ClientContextKey, &clientCtx)
	rootCmd.SetContext(cmdCtx)

	initSDKConfig()
	sdkversion.Name = app.Name
	sdkversion.AppName = app.Name + "d"
	sdkversion.Version = "development"
	rootCmd.PersistentFlags().String(flagsHome, app.DefaultNodeHome, "directory for config and data")
	rootCmd.PersistentFlags().String(flagsChainID, "g503-local-1", "chain ID")

	server.AddCommands(rootCmd, app.DefaultNodeHome, app.NewAppCreator(), app.NewAppExporter(), func(_ *cobra.Command) {})
	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(opsCmd())
	return rootCmd
}

const (
	flagsHome    = "home"
	flagsChainID = "chain-id"
)

func initSDKConfig() {
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount(app.AccountAddressPrefix, app.AccountAddressPrefix+"pub")
	cfg.SetBech32PrefixForValidator(app.AccountAddressPrefix+"valoper", app.AccountAddressPrefix+"valoperpub")
	cfg.SetBech32PrefixForConsensusNode(app.AccountAddressPrefix+"valcons", app.AccountAddressPrefix+"valconspub")
	cfg.Seal()
}
