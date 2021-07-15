package service

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"

	"github.com/saantiaguilera/liquidity-AX-50/pkg/domain"
	"github.com/saantiaguilera/liquidity-AX-50/third_party/erc20"
)

type (
	UniswapLiquidity struct {
		ethClient    uniswapLiquidityETHClient
		sniperClient uniswapLiquiditySniperClient

		sniperTTBAddr     common.Address
		sniperTTBTkn      *erc20.Erc20
		sniperTokenPaired common.Address
		sniperMinLiq      *big.Int
		sniperChainID     *big.Int
	}

	uniswapLiquidityETHClient interface {
		bind.ContractBackend

		NetworkID(context.Context) (*big.Int, error)
	}

	uniswapLiquiditySniperClient interface {
		Snipe(context.Context, *big.Int) error
	}

	uniswapAddLiquidityInput struct {
		TokenAddressA       common.Address
		TokenAddressB       common.Address
		AmountTokenADesired *big.Int
		AmountTokenBDesired *big.Int
		AmountTokenAMin     *big.Int
		AmountTokenBMin     *big.Int
		Deadline            *big.Int
		To                  common.Address
	}

	uniswapAddLiquidityETHInput struct {
		TokenAddress       common.Address
		AmountTokenDesired *big.Int
		AmountTokenMin     *big.Int
		AmountETHMin       *big.Int
		Deadline           *big.Int
		To                 common.Address
	}
)

func NewUniswapLiquidity(
	e uniswapLiquidityETHClient,
	s uniswapLiquiditySniperClient,
	sn domain.Sniper,
) (*UniswapLiquidity, error) {

	ttb := common.HexToAddress(sn.AddressTargetToken)
	ttbTkn, err := erc20.NewErc20(ttb, e)
	if err != nil {
		return nil, err
	}
	tp := common.HexToAddress(sn.AddressBaseCurrency)

	return &UniswapLiquidity{
		ethClient:         e,
		sniperClient:      s,
		sniperTTBAddr:     ttb,
		sniperTTBTkn:      ttbTkn,
		sniperTokenPaired: tp,
		sniperMinLiq:      sn.MinimumLiquidity,
		sniperChainID:     sn.ChainID,
	}, nil
}

func (u *UniswapLiquidity) newInputFromTx(tx *types.Transaction) uniswapAddLiquidityInput {
	data := tx.Data()[4:]
	tokenA := common.BytesToAddress(data[12:32])
	tokenB := common.BytesToAddress(data[44:64])
	var amountTokenADesired = new(big.Int)
	amountTokenADesired.SetString(common.Bytes2Hex(data[64:96]), 16)
	var amountTokenBDesired = new(big.Int)
	amountTokenBDesired.SetString(common.Bytes2Hex(data[96:128]), 16)
	var amountTokenAMin = new(big.Int)
	amountTokenAMin.SetString(common.Bytes2Hex(data[128:160]), 16)
	var amountTokenBMin = new(big.Int)
	amountTokenBMin.SetString(common.Bytes2Hex(data[160:192]), 16)
	to := common.BytesToAddress(data[204:224])
	var deadline = new(big.Int)
	deadline.SetString(common.Bytes2Hex(data[224:256]), 16)

	return uniswapAddLiquidityInput{
		TokenAddressA:       tokenA,
		TokenAddressB:       tokenB,
		AmountTokenADesired: amountTokenADesired,
		AmountTokenBDesired: amountTokenBDesired,
		AmountTokenAMin:     amountTokenAMin,
		AmountTokenBMin:     amountTokenBMin,
		Deadline:            deadline,
		To:                  to,
	}
}

func (u *UniswapLiquidity) newETHInputFromTx(tx *types.Transaction) uniswapAddLiquidityETHInput {
	data := tx.Data()[4:]
	token := common.BytesToAddress(data[12:32])
	var amountTokenDesired = new(big.Int)
	amountTokenDesired.SetString(common.Bytes2Hex(data[32:64]), 16)
	var amountTokenMin = new(big.Int)
	amountTokenMin.SetString(common.Bytes2Hex(data[64:96]), 16)
	var amountETHMin = new(big.Int)
	amountETHMin.SetString(common.Bytes2Hex(data[96:128]), 16)

	to := common.BytesToAddress(data[140:160])
	var deadline = new(big.Int)
	deadline.SetString(common.Bytes2Hex(data[160:192]), 16)

	return uniswapAddLiquidityETHInput{
		TokenAddress:       token,
		AmountTokenDesired: amountTokenDesired,
		AmountETHMin:       amountETHMin,
		AmountTokenMin:     amountTokenMin,
		Deadline:           deadline,
		To:                 to,
	}
}

func (u *UniswapLiquidity) getTxSenderAddressQuick(tx *types.Transaction) (common.Address, error) {
	msg, err := tx.AsMessage(types.NewEIP155Signer(u.sniperChainID), nil)
	if err != nil {
		return common.Address{}, err
	}
	return msg.From(), nil
}

func (u *UniswapLiquidity) getTokenSymbol(tokenAddress common.Address) string {
	tokenIntance, _ := erc20.NewErc20(tokenAddress, u.ethClient)
	sym, err := tokenIntance.Symbol(nil)
	if err != nil {
		return err.Error()
	}
	return sym
}

func (u *UniswapLiquidity) Add(ctx context.Context, tx *types.Transaction) error {
	sender, err := u.getTxSenderAddressQuick(tx)
	if err != nil {
		return err
	}

	// parse the info of the swap so that we can access it easily
	var addLiquidity = u.newInputFromTx(tx)

	// security checks
	// does the liquidity addition deals with the token i'm targetting?
	if addLiquidity.TokenAddressA == u.sniperTTBAddr || addLiquidity.TokenAddressB == u.sniperTTBAddr {
		// does the liquidity is added on the right pair?
		if addLiquidity.TokenAddressA == u.sniperTokenPaired || addLiquidity.TokenAddressB == u.sniperTokenPaired {
			tknBalanceSender, err := u.sniperTTBTkn.BalanceOf(nil, sender)
			if err != nil {
				return err
			}

			var amountTknMin *big.Int
			var amountPairedMin *big.Int
			if addLiquidity.TokenAddressA == u.sniperTTBAddr {
				amountTknMin = addLiquidity.AmountTokenAMin
				amountPairedMin = addLiquidity.AmountTokenBMin
			} else {
				amountTknMin = addLiquidity.AmountTokenBMin
				amountPairedMin = addLiquidity.AmountTokenAMin
			}
			// we check if the liquidity provider really possess the liquidity he wants to add, because it is possible tu be lured by other bots that fake liquidity addition.
			checkBalanceTknLP := amountTknMin.Cmp(tknBalanceSender)
			if checkBalanceTknLP == 0 || checkBalanceTknLP == -1 {
				// we check if the liquidity provider add enough collateral (WBNB or BUSD) as expected by our configuration. Bc sometimes the dev fuck the pleb and add way less liquidity that was advertised on telegram.
				if amountPairedMin.Cmp(u.sniperMinLiq) == 1 {
					return u.sniperClient.Snipe(ctx, tx.GasPrice())
				} else {
					log.Info(fmt.Sprintf(
						"liquidity added but lower than expected: %.4f %s vs %.4f expected",
						formatETHWeiToEther(amountPairedMin),
						u.getTokenSymbol(u.sniperTokenPaired),
						formatETHWeiToEther(u.sniperMinLiq),
					))
				}
			}
		}
	}
	return nil
}

// interest Sniping and filter addliquidity tx
// TODO Super similars, refactor?
func (u *UniswapLiquidity) AddETH(ctx context.Context, tx *types.Transaction) error {
	// parse the info of the swap so that we can access it easily
	sender, err := u.getTxSenderAddressQuick(tx)
	if err != nil {
		return err
	}

	addLiquidity := u.newETHInputFromTx(tx)

	tknBalanceSender, err := u.sniperTTBTkn.BalanceOf(nil, sender)
	if err != nil {
		return err
	}

	checkBalanceLP := addLiquidity.AmountTokenMin.Cmp(tknBalanceSender)

	// security checks:
	// does the liquidity addition deals with the token i'm targetting?
	if addLiquidity.TokenAddress == u.sniperTTBAddr {
		// we check if the liquidity provider really possess the liquidity he wants to add, because it is possible tu be lured by other bots that fake liquidity addition.
		if checkBalanceLP == 0 || checkBalanceLP == -1 {
			// we check if the liquidity provider add enough collateral (WBNB or BUSD) as expected by our configuration. Bc sometimes the dev fuck the pleb and add way less liquidity that was advertised on telegram.
			if tx.Value().Cmp(u.sniperMinLiq) == 1 {
				if addLiquidity.AmountETHMin.Cmp(u.sniperMinLiq) == 1 {
					return u.sniperClient.Snipe(ctx, tx.GasPrice())
				}
			} else {
				log.Info(fmt.Sprintf(
					"liquidity network (BNB/ETH?) added but lower than expected: %.4f vs %.4f expected",
					formatETHWeiToEther(tx.Value()),
					formatETHWeiToEther(u.sniperMinLiq),
				))
			}
		}
	}
	return nil
}

func formatETHWeiToEther(etherAmount *big.Int) float64 {
	var base, exponent = big.NewInt(10), big.NewInt(18)
	denominator := base.Exp(base, exponent, nil)
	// Convert to float for precision
	tokensSentFloat := new(big.Float).SetInt(etherAmount)
	denominatorFloat := new(big.Float).SetInt(denominator)
	// Divide and return the final result
	final, _ := new(big.Float).Quo(tokensSentFloat, denominatorFloat).Float64()
	return final
}