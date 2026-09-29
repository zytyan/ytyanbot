package handlers

import (
	"fmt"
	"main/globalcfg/h"
	"main/helpers/mathparser"
	"math/big"
	"strconv"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

var smallNumberThreshold = big.NewRat(1, 20000)

func ratToText(r *big.Rat) string {
	fixed := strings.TrimRight(strings.TrimRight(r.FloatString(4), "0"), ".")
	if r.Sign() == 0 {
		return fixed
	}

	abs := new(big.Rat).Abs(r)
	if abs.Cmp(smallNumberThreshold) >= 0 {
		return fixed
	}

	return ratToScientificText(r, 4)
}

func ratToScientificText(r *big.Rat, significantDigits int) string {
	abs := new(big.Rat).Abs(r)
	exponent := len(abs.Num().String()) - len(abs.Denom().String())

	var power, scaled big.Int
	power.Exp(big.NewInt(10), big.NewInt(int64(absInt(exponent))), nil)
	if exponent >= 0 {
		scaled.Mul(abs.Denom(), &power)
		if abs.Num().Cmp(&scaled) < 0 {
			exponent--
		}
	} else {
		scaled.Mul(abs.Num(), &power)
		if scaled.Cmp(abs.Denom()) < 0 {
			exponent--
		}
	}

	power.Exp(big.NewInt(10), big.NewInt(int64(absInt(exponent))), nil)
	scale := new(big.Rat).SetInt(&power)
	mantissa := new(big.Rat).Set(abs)
	if exponent < 0 {
		mantissa.Mul(mantissa, scale)
	} else {
		mantissa.Quo(mantissa, scale)
	}

	digitsAfterDecimal := significantDigits - 1
	mantissaText := mantissa.FloatString(digitsAfterDecimal)
	if strings.HasPrefix(mantissaText, "10.") || mantissaText == "10" {
		exponent++
		mantissaText = new(big.Rat).SetFrac64(1, 1).FloatString(digitsAfterDecimal)
	}
	mantissaText = strings.TrimRight(strings.TrimRight(mantissaText, "0"), ".")
	if r.Sign() < 0 {
		mantissaText = "-" + mantissaText
	}

	return mantissaText + "e" + strconv.Itoa(exponent)
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func SolveMath(bot *gotgbot.Bot, ctx *ext.Context) (err error) {
	text := ctx.Message.Text
	force := false
	text = mathReplacer.Replace(text)
	if strings.HasPrefix(text, "/") {
		text = h.TrimCmd(text)
		force = true
	}
	res, err := mathparser.Evaluate(text)
	if err != nil {
		if force {
			_, _ = ctx.EffectiveMessage.Reply(bot,
				fmt.Sprintf("计算失败, error: %s", err.Error()),
				nil)
		}
		return nil
	}
	_, _ = ctx.EffectiveMessage.Reply(bot,
		fmt.Sprintf("%s = %s", text, ratToText(res)), nil)
	return
}

var mathReplacer = func() *strings.Replacer {
	src := "（）【】！￥，。？“”‘’～"
	dst := "()[]!$,.?\"\"''~"
	srcL := make([]rune, 0, len(dst))
	dstL := make([]rune, 0, len(dst))
	for _, r := range src {
		srcL = append(srcL, r)
	}
	for _, r := range dst {
		dstL = append(dstL, r)
	}
	if len(srcL) != len(dstL) {
		panic("len(srcL) != len(dstL)")
	}
	outL := make([]string, 0, len(dst)*2)
	for i, r := range srcL {
		outL = append(outL, string(r), string(dstL[i]))
	}
	replacer := strings.NewReplacer(outL...)
	return replacer
}()

func NeedSolve(msg *gotgbot.Message) bool {
	if !chatCfg(msg.Chat.Id).AutoCalculate {
		return false
	}
	text := msg.Text
	if strings.HasPrefix(text, "/") {
		return false
	}
	return mathparser.FastCheck(text)
}
