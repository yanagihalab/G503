package chainclient

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/stretchr/testify/require"

	g503app "github.com/yanagihalab/G503/app"
	sanctiontypes "github.com/yanagihalab/G503/x/sanction/types"
)

func TestTxBuilderRejectsInvalidPrivateScalars(t *testing.T) {
	for _, key := range []string{strings.Repeat("00", 32), "fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", strings.Repeat("ff", 32)} {
		_, err := privateKeyFromHex(key)
		require.Error(t, err)
	}
}

func TestEncodeUnsignedTx(t *testing.T) {
	txBytes, err := EncodeUnsignedTx(&sanctiontypes.MsgSubmitRiskReport{
		Submitter: "g5031qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnrql8a",
		Report: &sanctiontypes.RiskReport{
			ReportId: "report-1",
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, txBytes)

	encoding := g503app.MakeEncodingConfig()
	decoded, err := encoding.TxConfig.TxDecoder()(txBytes)
	require.NoError(t, err)
	require.Len(t, decoded.GetMsgs(), 1)
	require.IsType(t, &sanctiontypes.MsgSubmitRiskReport{}, decoded.GetMsgs()[0])
}

func TestEncodeSignedTx(t *testing.T) {
	txBytes, err := EncodeSignedTx(SignOptions{
		ChainID:       "g503-test-1",
		PrivateKeyHex: "0101010101010101010101010101010101010101010101010101010101010101",
		AccountNumber: 7,
		Sequence:      3,
		GasLimit:      100000,
		FeeAmount:     sdk.NewCoins(sdk.NewInt64Coin("stake", 1)),
	}, &sanctiontypes.MsgExecuteSanction{
		Executor: "g50310xcqpzrky6eff2g52qdye53xkk9jxkvrnsxwnj",
		CaseId:   "case-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, txBytes)

	encoding := g503app.MakeEncodingConfig()
	decoded, err := encoding.TxConfig.TxDecoder()(txBytes)
	require.NoError(t, err)
	require.Len(t, decoded.GetMsgs(), 1)
	sigs, err := decoded.(interface {
		GetSignaturesV2() ([]signing.SignatureV2, error)
	}).GetSignaturesV2()
	require.NoError(t, err)
	require.Len(t, sigs, 1)
	require.Equal(t, uint64(3), sigs[0].Sequence)
}
