package app

import (
	"context"
	"encoding/json"
	"io"

	coreaddress "cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"cosmossdk.io/log/v2"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	cmtservice "github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	nodeservice "github.com/cosmos/cosmos-sdk/client/grpc/node"
	"github.com/cosmos/cosmos-sdk/codec"
	authcodec "github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authante "github.com/cosmos/cosmos-sdk/x/auth/ante"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/bank"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	consensuskeeper "github.com/cosmos/cosmos-sdk/x/consensus/keeper"
	consensustypes "github.com/cosmos/cosmos-sdk/x/consensus/types"
	"github.com/cosmos/cosmos-sdk/x/slashing"
	slashingkeeper "github.com/cosmos/cosmos-sdk/x/slashing/keeper"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	"github.com/cosmos/cosmos-sdk/x/staking"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/cosmos/gogoproto/grpc"
	gogoproto "github.com/cosmos/gogoproto/proto"

	"github.com/yanagihalab/G503/x/sanction"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	sanctiontypes "github.com/yanagihalab/G503/x/sanction/types"
)

const (
	Name                 = "g503"
	AccountAddressPrefix = "g503"
	BondDenom            = "stake"
)

var DefaultNodeHome = "." + Name

type App struct {
	*baseapp.BaseApp

	legacyAmino       *codec.LegacyAmino
	appCodec          codec.Codec
	txConfig          client.TxConfig
	interfaceRegistry codectypes.InterfaceRegistry

	keys map[string]*storetypes.KVStoreKey

	AuthKeeper      authkeeper.AccountKeeper
	BankKeeper      bankkeeper.BaseKeeper
	StakingKeeper   *stakingkeeper.Keeper
	SlashingKeeper  slashingkeeper.Keeper
	ConsensusKeeper consensuskeeper.Keeper

	AuthModule      auth.AppModule
	BankModule      bank.AppModule
	StakingModule   staking.AppModule
	SlashingModule  slashing.AppModule
	ConsensusModule interface {
		InitGenesis(sdk.Context, codec.JSONCodec, json.RawMessage)
		ExportGenesis(sdk.Context, codec.JSONCodec) json.RawMessage
	}

	SanctionKeeper keeper.Keeper
	SanctionModule sanction.AppModule
}

func New(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	loadLatest bool,
	_ servertypes.AppOptions,
	baseAppOptions ...func(*baseapp.BaseApp),
) *App {
	encoding := MakeEncodingConfig()
	txDecoder := encoding.TxConfig.TxDecoder()
	keys := map[string]*storetypes.KVStoreKey{
		authtypes.StoreKey:       storetypes.NewKVStoreKey(authtypes.StoreKey),
		banktypes.StoreKey:       storetypes.NewKVStoreKey(banktypes.StoreKey),
		stakingtypes.StoreKey:    storetypes.NewKVStoreKey(stakingtypes.StoreKey),
		slashingtypes.StoreKey:   storetypes.NewKVStoreKey(slashingtypes.StoreKey),
		consensuskeeper.StoreKey: storetypes.NewKVStoreKey(consensuskeeper.StoreKey),
		sanctiontypes.StoreKey:   storetypes.NewKVStoreKey(sanctiontypes.StoreKey),
	}

	bApp := baseapp.NewBaseApp(Name, logger, db, txDecoder, baseAppOptions...)
	bApp.SetTxDecoder(txDecoder)
	bApp.SetTxEncoder(encoding.TxConfig.TxEncoder())
	bApp.SetInterfaceRegistry(encoding.InterfaceRegistry)
	bApp.MountStores(
		keys[authtypes.StoreKey],
		keys[banktypes.StoreKey],
		keys[stakingtypes.StoreKey],
		keys[slashingtypes.StoreKey],
		keys[consensuskeeper.StoreKey],
		keys[sanctiontypes.StoreKey],
	)

	accountCodec := authcodec.NewBech32Codec(AccountAddressPrefix)
	validatorCodec := authcodec.NewBech32Codec(AccountAddressPrefix + "valoper")
	consensusCodec := authcodec.NewBech32Codec(AccountAddressPrefix + "valcons")
	authority, err := accountCodec.BytesToString(authtypes.NewModuleAddress("gov"))
	if err != nil {
		panic(err)
	}
	maccPerms := moduleAccountPermissions()
	authKeeper := authkeeper.NewAccountKeeper(
		encoding.Codec,
		storeService(keys[authtypes.StoreKey]),
		authtypes.ProtoBaseAccount,
		maccPerms,
		accountCodec,
		AccountAddressPrefix,
		authority,
	)
	bankKeeper := bankkeeper.NewBaseKeeper(
		encoding.Codec,
		storeService(keys[banktypes.StoreKey]),
		authKeeper,
		blockedAccountAddrs(accountCodec, maccPerms),
		authority,
		logger,
	)
	stakingKeeper := stakingkeeper.NewKeeper(
		encoding.Codec,
		storeService(keys[stakingtypes.StoreKey]),
		authKeeper,
		bankKeeper,
		authority,
		validatorCodec,
		consensusCodec,
	)
	slashingKeeper := slashingkeeper.NewKeeper(
		encoding.Codec,
		encoding.LegacyAmino,
		storeService(keys[slashingtypes.StoreKey]),
		stakingKeeper,
		authority,
	)
	stakingKeeper.SetHooks(slashingKeeper.Hooks())
	consensusKeeper := consensuskeeper.NewKeeper(
		encoding.Codec,
		storeService(keys[consensuskeeper.StoreKey]),
		authority,
		nil,
	)
	bApp.SetParamStore(consensusKeeper.ParamsStore)

	sanctionKeeper := keeper.NewKeeper(
		keys[sanctiontypes.StoreKey],
		authority,
		"",
	).WithBankKeeper(bankKeeper)
	bankKeeper.AppendSendRestriction(func(ctx context.Context, from, to sdk.AccAddress, _ sdk.Coins) (sdk.AccAddress, error) {
		return to, sanctionKeeper.SendCoinsAllowed(sdk.UnwrapSDKContext(ctx), from, to)
	})
	authModule := auth.NewAppModule(encoding.Codec, authKeeper, nil, nil)
	bankModule := bank.NewAppModule(encoding.Codec, bankKeeper, authKeeper, nil)
	stakingModule := staking.NewAppModule(encoding.Codec, stakingKeeper, authKeeper, bankKeeper, nil)
	slashingModule := slashing.NewAppModule(encoding.Codec, slashingKeeper, authKeeper, bankKeeper, stakingKeeper, nil, encoding.InterfaceRegistry)
	sanctionModule := sanction.NewAppModule(sanctionKeeper)
	configurator := module.NewConfigurator(
		encoding.Codec,
		bApp.MsgServiceRouter(),
		bApp.GRPCQueryRouter(),
	)
	authModule.RegisterServices(configurator)
	bankModule.RegisterServices(configurator)
	stakingModule.RegisterServices(configurator)
	slashingModule.RegisterServices(configurator)
	consensustypes.RegisterMsgServer(bApp.MsgServiceRouter(), consensusKeeper)
	consensustypes.RegisterQueryServer(bApp.GRPCQueryRouter(), consensusKeeper)
	sanctionModule.RegisterServices(configurator)
	anteHandler, err := authante.NewAnteHandler(authante.HandlerOptions{
		AccountKeeper:   authKeeper,
		BankKeeper:      bankKeeper,
		SignModeHandler: encoding.TxConfig.SignModeHandler(),
		SigGasConsumer:  authante.DefaultSigVerificationGasConsumer,
	})
	if err != nil {
		panic(err)
	}
	bApp.SetAnteHandler(func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		if err := sanctionKeeper.CheckAgentLeaseTx(ctx, tx); err != nil {
			return ctx, err
		}
		return anteHandler(ctx, tx, simulate)
	})
	bApp.SetPostHandler(sanctionKeeper.RecordOriginalTransfers)

	app := &App{
		BaseApp:           bApp,
		legacyAmino:       encoding.LegacyAmino,
		appCodec:          encoding.Codec,
		txConfig:          encoding.TxConfig,
		interfaceRegistry: encoding.InterfaceRegistry,
		keys:              keys,
		AuthKeeper:        authKeeper,
		BankKeeper:        bankKeeper,
		StakingKeeper:     stakingKeeper,
		SlashingKeeper:    slashingKeeper,
		ConsensusKeeper:   consensusKeeper,
		AuthModule:        authModule,
		BankModule:        bankModule,
		StakingModule:     stakingModule,
		SlashingModule:    slashingModule,
		SanctionKeeper:    sanctionKeeper,
		SanctionModule:    sanctionModule,
	}

	bApp.SetPrepareProposal(sanctionKeeper.PrepareProposalHandler())
	bApp.SetProcessProposal(sanctionKeeper.ProcessProposalHandler())
	bApp.SetInitChainer(app.InitChainer)
	bApp.SetBeginBlocker(func(ctx sdk.Context) (sdk.BeginBlock, error) {
		if app.CanRunSlashingBeginBlock(ctx) {
			if err := slashingModule.BeginBlock(ctx); err != nil {
				return sdk.BeginBlock{}, err
			}
		}
		if err := stakingModule.BeginBlock(ctx); err != nil {
			return sdk.BeginBlock{}, err
		}
		return sdk.BeginBlock{}, nil
	})
	bApp.SetEndBlocker(func(ctx sdk.Context) (sdk.EndBlock, error) {
		updates, err := stakingModule.EndBlock(ctx)
		if err != nil {
			return sdk.EndBlock{}, err
		}
		if err := app.SyncSanctionAgentsWithStakingValidators(ctx); err != nil {
			return sdk.EndBlock{}, err
		}
		return sdk.EndBlock{ValidatorUpdates: updates}, err
	})

	if err := bApp.LoadLatestVersion(); err != nil {
		panic(err)
	}
	if !loadLatest {
		_ = traceStore
	}
	return app
}

func (app *App) InitChainer(ctx sdk.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	var appState map[string]json.RawMessage
	if len(req.AppStateBytes) > 0 {
		if err := json.Unmarshal(req.AppStateBytes, &appState); err != nil {
			return nil, err
		}
	}
	genesis := sanctiontypes.DefaultGenesis()
	if bz, ok := appState[sanctiontypes.ModuleName]; ok && len(bz) > 0 {
		app.appCodec.MustUnmarshalJSON(bz, genesis)
	}
	if err := sanctiontypes.ValidateGenesis(genesis); err != nil {
		return nil, err
	}
	app.AuthKeeper.InitGenesis(ctx, readAuthGenesis(app.appCodec, appState))
	app.BankKeeper.InitGenesis(ctx, readBankGenesis(app.appCodec, appState))
	stakingGenesis := readStakingGenesis(app.appCodec, appState)
	updates := app.StakingKeeper.InitGenesis(ctx, stakingGenesis)
	app.SlashingModule.InitGenesis(ctx, app.appCodec, readRawGenesis(app.appCodec, appState, slashingtypes.ModuleName, slashingtypes.DefaultGenesisState()))
	if len(updates) == 0 && len(req.Validators) > 0 {
		updates = req.Validators
	}
	if req.ConsensusParams != nil {
		if err := app.ConsensusKeeper.ParamsStore.Set(ctx, *req.ConsensusParams); err != nil {
			return nil, err
		}
	}
	app.SanctionKeeper.InitGenesis(ctx, genesis)
	return &abci.ResponseInitChain{Validators: updates}, nil
}

func (app *App) ExportAppStateAndValidators() (servertypes.ExportedApp, error) {
	state := map[string]json.RawMessage{
		authtypes.ModuleName:     app.appCodec.MustMarshalJSON(app.AuthKeeper.ExportGenesis(app.NewContext(false))),
		banktypes.ModuleName:     app.appCodec.MustMarshalJSON(app.BankKeeper.ExportGenesis(app.NewContext(false))),
		stakingtypes.ModuleName:  app.appCodec.MustMarshalJSON(app.StakingKeeper.ExportGenesis(app.NewContext(false))),
		slashingtypes.ModuleName: app.SlashingModule.ExportGenesis(app.NewContext(false), app.appCodec),
		sanctiontypes.ModuleName: app.SanctionModule.ExportGenesis(app.NewContext(false), app.appCodec),
	}
	bz, err := json.Marshal(state)
	if err != nil {
		return servertypes.ExportedApp{}, err
	}
	return servertypes.ExportedApp{
		AppState:        bz,
		Validators:      nil,
		Height:          app.LastBlockHeight(),
		ConsensusParams: cmtproto.ConsensusParams{},
	}, nil
}

func storeService(key *storetypes.KVStoreKey) corestore.KVStoreService {
	return runtime.NewKVStoreService(key)
}

func moduleAccountPermissions() map[string][]string {
	return map[string][]string{
		authtypes.FeeCollectorName:     nil,
		stakingtypes.BondedPoolName:    {authtypes.Burner, authtypes.Staking},
		stakingtypes.NotBondedPoolName: {authtypes.Burner, authtypes.Staking},
		sanctiontypes.ModuleName:       nil,
	}
}

func blockedAccountAddrs(accountCodec coreaddress.Codec, perms map[string][]string) map[string]bool {
	blocked := map[string]bool{}
	for name := range perms {
		if name == sanctiontypes.ModuleName {
			continue
		}
		addr, err := accountCodec.BytesToString(authtypes.NewModuleAddress(name))
		if err != nil {
			panic(err)
		}
		blocked[addr] = true
	}
	return blocked
}

func readAuthGenesis(cdc codec.Codec, appState map[string]json.RawMessage) authtypes.GenesisState {
	genesis := authtypes.DefaultGenesisState()
	if bz, ok := appState[authtypes.ModuleName]; ok && len(bz) > 0 {
		cdc.MustUnmarshalJSON(bz, genesis)
	}
	return *genesis
}

func readBankGenesis(cdc codec.Codec, appState map[string]json.RawMessage) *banktypes.GenesisState {
	genesis := banktypes.DefaultGenesisState()
	if bz, ok := appState[banktypes.ModuleName]; ok && len(bz) > 0 {
		cdc.MustUnmarshalJSON(bz, genesis)
	}
	return genesis
}

func readStakingGenesis(cdc codec.Codec, appState map[string]json.RawMessage) *stakingtypes.GenesisState {
	genesis := stakingtypes.DefaultGenesisState()
	genesis.Params.BondDenom = BondDenom
	if bz, ok := appState[stakingtypes.ModuleName]; ok && len(bz) > 0 {
		cdc.MustUnmarshalJSON(bz, genesis)
	}
	return genesis
}

func readRawGenesis(cdc codec.Codec, appState map[string]json.RawMessage, moduleName string, defaultGenesis gogoproto.Message) json.RawMessage {
	if bz, ok := appState[moduleName]; ok && len(bz) > 0 {
		return bz
	}
	return cdc.MustMarshalJSON(defaultGenesis)
}

func (app *App) SyncSanctionAgentsWithStakingValidators(ctx sdk.Context) error {
	validators, err := app.StakingKeeper.GetAllValidators(ctx)
	if err != nil {
		return err
	}
	if len(validators) == 0 {
		return nil
	}
	states := make([]keeper.ValidatorAgentState, 0, len(validators))
	for _, validator := range validators {
		valAddr, err := sdk.ValAddressFromBech32(validator.GetOperator())
		if err != nil {
			return err
		}
		power := validator.ConsensusPower(sdk.DefaultPowerReduction)
		active := validator.IsBonded() && !validator.IsJailed() && power > 0
		if !active {
			power = 0
		}
		var publicKey []byte
		if account := app.AuthKeeper.GetAccount(ctx, sdk.AccAddress(valAddr)); account != nil && account.GetPubKey() != nil {
			publicKey = account.GetPubKey().Bytes()
		}
		states = append(states, keeper.ValidatorAgentState{
			ValidatorOperatorAddress: validator.GetOperator(),
			SignerAddress:            sdk.AccAddress(valAddr).String(),
			PublicKey:                publicKey,
			VotingPower:              uint64(power),
			Active:                   active,
			MetadataURI:              "staking-sync",
		})
	}
	app.SanctionKeeper.SyncAgentRegistryWithValidatorSet(ctx, states)
	return nil
}

func (app *App) CanRunSlashingBeginBlock(ctx sdk.Context) bool {
	voteInfos := ctx.VoteInfos()
	if len(voteInfos) == 0 {
		return false
	}
	for _, voteInfo := range voteInfos {
		if _, err := app.StakingKeeper.ValidatorByConsAddr(ctx, sdk.ConsAddress(voteInfo.Validator.Address)); err != nil {
			return false
		}
	}
	return true
}

func (app *App) LegacyAmino() *codec.LegacyAmino {
	return app.legacyAmino
}

func (app *App) AppCodec() codec.Codec {
	return app.appCodec
}

func (app *App) InterfaceRegistry() codectypes.InterfaceRegistry {
	return app.interfaceRegistry
}

func (app *App) TxConfig() client.TxConfig {
	return app.txConfig
}

func (app *App) GetKey(storeKey string) *storetypes.KVStoreKey {
	return app.keys[storeKey]
}

func (app *App) RegisterAPIRoutes(apiSvr *api.Server, _ config.APIConfig) {
	clientCtx := apiSvr.ClientCtx
	ctx := context.Background()
	authtx.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)
	if err := authtypes.RegisterQueryHandlerClient(ctx, apiSvr.GRPCGatewayRouter, authtypes.NewQueryClient(clientCtx)); err != nil {
		panic(err)
	}
	if err := banktypes.RegisterQueryHandlerClient(ctx, apiSvr.GRPCGatewayRouter, banktypes.NewQueryClient(clientCtx)); err != nil {
		panic(err)
	}
	if err := stakingtypes.RegisterQueryHandlerClient(ctx, apiSvr.GRPCGatewayRouter, stakingtypes.NewQueryClient(clientCtx)); err != nil {
		panic(err)
	}
	if err := slashingtypes.RegisterQueryHandlerClient(ctx, apiSvr.GRPCGatewayRouter, slashingtypes.NewQueryClient(clientCtx)); err != nil {
		panic(err)
	}
	if err := consensustypes.RegisterQueryHandlerClient(ctx, apiSvr.GRPCGatewayRouter, consensustypes.NewQueryClient(clientCtx)); err != nil {
		panic(err)
	}
	cmtservice.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)
	nodeservice.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)
	sanction.AppModuleBasic{}.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)
}

func (app *App) RegisterGRPCServer(server grpc.Server) {
	app.BaseApp.RegisterGRPCServer(server)
}

func (app *App) RegisterTxService(clientCtx client.Context) {
	authtx.RegisterTxService(app.GRPCQueryRouter(), clientCtx, app.Simulate, app.interfaceRegistry)
}

func (app *App) RegisterTendermintService(clientCtx client.Context) {
	cmtApp := server.NewCometABCIWrapper(app)
	cmtservice.RegisterTendermintService(clientCtx, app.GRPCQueryRouter(), app.interfaceRegistry, cmtApp.Query)
}

func (app *App) RegisterNodeService(clientCtx client.Context, cfg config.Config) {
	nodeservice.RegisterNodeService(clientCtx, app.GRPCQueryRouter(), cfg, func() int64 {
		return app.CommitMultiStore().EarliestVersion()
	})
}

func (app *App) Close() error {
	return nil
}

func NewAppCreator() servertypes.AppCreator {
	return func(logger log.Logger, db dbm.DB, appOpts servertypes.AppOptions) servertypes.Application {
		return New(logger, db, nil, true, appOpts, server.DefaultBaseappOptions(appOpts)...)
	}
}

func NewAppExporter() servertypes.AppExporter {
	return func(
		logger log.Logger,
		db dbm.DB,
		height int64,
		_ bool,
		_ []string,
		opts servertypes.AppOptions,
		_ []string,
	) (servertypes.ExportedApp, error) {
		g503App := New(logger, db, nil, true, opts, server.DefaultBaseappOptions(opts)...)
		if height != -1 {
			if err := g503App.LoadVersion(height); err != nil {
				return servertypes.ExportedApp{}, err
			}
		}
		return g503App.ExportAppStateAndValidators()
	}
}
