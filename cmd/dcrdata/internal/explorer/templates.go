// Copyright (c) 2018-2022, The Decred developers
// Copyright (c) 2017, The dcrdata developers
// See LICENSE for details.

package explorer

import (
	"encoding/hex"
	"fmt"
	"html/template"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrdata/exchanges/v3"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/decred/dcrdata/v8/explorer/types"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/txhelpers"
	"github.com/decred/dcrdata/v8/utils"
	humanize "github.com/dustin/go-humanize"
)

type pageTemplate struct {
	file     string
	template *template.Template
}

type templates struct {
	templates map[string]pageTemplate
	common    []string
	folder    string
	helpers   template.FuncMap
	exec      func(string, interface{}) (string, error)
}

func newTemplates(folder string, reload bool, common []string, helpers template.FuncMap) templates {
	com := make([]string, 0, len(common))
	for _, file := range common {
		com = append(com, filepath.Join(folder, file+".tmpl"))
	}
	t := templates{
		templates: make(map[string]pageTemplate),
		common:    com,
		folder:    folder,
		helpers:   helpers,
	}
	t.exec = t.execTemplateToString
	if reload {
		t.exec = t.execWithReload
	}

	return t
}

func (t *templates) addTemplate(name string) error {
	fileName := filepath.Join(t.folder, name+".tmpl")
	files := append(t.common, fileName)
	temp, err := template.New(name).Funcs(t.helpers).ParseFiles(files...)
	if err == nil {
		t.templates[name] = pageTemplate{
			file:     fileName,
			template: temp,
		}
	}
	return err
}

func (t *templates) reloadTemplates() error {
	var errorStrings []string
	for fileName := range t.templates {
		err := t.addTemplate(fileName)
		if err != nil {
			errorStrings = append(errorStrings, err.Error())
		}
	}
	if errorStrings == nil {
		return nil
	}
	return fmt.Errorf(strings.Join(errorStrings, " | "))
}

// execTemplateToString executes the associated input template using the
// supplied data, and writes the result into a string. If the template fails to
// execute or isn't found, a non-nil error will be returned. Check it before
// writing to theclient, otherwise you might as well execute directly into
// your response writer instead of the internal buffer of this function.
func (t *templates) execTemplateToString(name string, data interface{}) (string, error) {
	temp, ok := t.templates[name]
	if !ok {
		return "", fmt.Errorf("Template %s not known", name)
	}
	var page strings.Builder
	err := temp.template.ExecuteTemplate(&page, name, data)
	return page.String(), err
}

// execWithReload is the same as execTemplateToString, but will reload the
// template first.
func (t *templates) execWithReload(name string, data interface{}) (string, error) {
	err := t.addTemplate(name)
	if err != nil {
		return "", fmt.Errorf("execWithReload: %v", err)
	}
	log.Debugf("reloaded HTML template %q", name)
	return t.execTemplateToString(name, data)
}

var toInt64 = func(v interface{}) int64 {
	switch vt := v.(type) {
	case int64:
		return vt
	case int32:
		return int64(vt)
	case uint32:
		return int64(vt)
	case uint64:
		return int64(vt)
	case int:
		return int64(vt)
	case int16:
		return int64(vt)
	case uint16:
		return int64(vt)
	default:
		return math.MinInt64
	}
}

// float64Formatting formats a float64 value into multiple strings depending on whether
// boldNumPlaces is provided or not. boldNumPlaces defines the number of decimal
// places to be written with same font as the whole number value of the float.
// If boldNumPlaces is provided the returned slice should have at least four items
// otherwise it should have at least three items. i.e. given v is to 342.12132000,
// numplaces is 8 and boldNumPlaces is set to 2 the following should be returned
// []string{"342", "12", "132", "000"}. If boldNumPlace is not set the returned
// slice should be []string{"342", "12132", "000"}.
func float64Formatting(v float64, numPlaces int, useCommas bool, boldNumPlaces ...int) []string {
	pow := math.Pow(10, float64(numPlaces))
	formattedVal := math.Round(v*pow) / pow
	clipped := fmt.Sprintf("%."+strconv.Itoa(numPlaces)+"f", formattedVal)
	oldLength := len(clipped)
	clipped = strings.TrimRight(clipped, "0")
	trailingZeros := strings.Repeat("0", oldLength-len(clipped))
	valueChunks := strings.Split(clipped, ".")
	integer := valueChunks[0]

	dec := ""
	if len(valueChunks) > 1 {
		dec = valueChunks[1]
	}

	if useCommas {
		integer = humanize.Comma(int64(formattedVal))
	}

	if len(boldNumPlaces) == 0 {
		return []string{integer, dec, trailingZeros}
	}

	places := boldNumPlaces[0]
	if places > numPlaces {
		return []string{integer, dec, trailingZeros}
	}

	if len(dec) < places {
		places = len(dec)
	}

	return []string{integer, dec[:places], dec[places:], trailingZeros}
}

// within the limit of hashrate. Can be set from gigahash to Petahash
func hashrateFormatting(v float64, numPlaces int, useCommas bool, boldNumPlaces ...int) []string {
	if 0.001 <= v && v < 1 {
		v = v * 1000
	}
	if v < 0.001 {
		v = v * math.Pow(10, 6)
	}

	return float64Formatting(v, numPlaces, useCommas, boldNumPlaces...)
}

func hashratePostfix(v float64) string {
	if 0.001 <= v && v < 1 {
		return "TH/s"
	}
	if v < 0.001 {
		return "GH/s"
	}
	return "PH/s"
}

func difficultyDisp(d float64) string {
	if d < 1e5 {
		return float64Formatting(d, 0, true)[0]
	}
	if d < 1e6 {
		diff := float64(d / 1e3)
		format := float64Formatting(diff, 2, true)
		return fmt.Sprintf("%s.%sK", format[0], format[1])
	}
	if d < 1e9 {
		diff := float64(d / 1e6)
		format := float64Formatting(diff, 2, true)
		return fmt.Sprintf("%s.%sM", format[0], format[1])
	}
	diff := float64(d / 1e9)
	format := float64Formatting(diff, 2, true)
	return fmt.Sprintf("%s.%sB", format[0], format[1])
}

func diffFormat(d float64) string {
	if d < 1e4 {
		return float64Formatting(d, 0, true)[0]
	}
	if d < 1e6 {
		diff := float64(d / 1e3)
		format := float64Formatting(diff, 2, true)
		return fmt.Sprintf("%s.%sK", format[0], format[1])
	}
	if d < 1e9 {
		diff := float64(d / 1e6)
		format := float64Formatting(diff, 2, true)
		return fmt.Sprintf("%s.%sM", format[0], format[1])
	}
	if d < 1e12 {
		diff := float64(d / 1e9)
		format := float64Formatting(diff, 2, true)
		return fmt.Sprintf("%s.%sG", format[0], format[1])
	}

	diff := float64(d / 1e12)
	format := float64Formatting(diff, 2, true)
	return fmt.Sprintf("%s.%sT", format[0], format[1])
}

func simpleHashrateDisp(hashrate float64) string {
	if hashrate < 1e4 {
		return float64Formatting(hashrate, 0, true)[0]
	}
	var hash float64
	if hashrate < 1e6 {
		hash = float64(hashrate / 1e3)
	} else if hashrate < 1e9 {
		hash = float64(hashrate / 1e6)
	} else if hashrate < 1e12 {
		hash = float64(hashrate / 1e9)
	} else if hashrate < 1e15 {
		hash = float64(hashrate / 1e12)
	} else if hashrate < 1e18 {
		hash = float64(hashrate / 1e15)
	} else {
		hash = float64(hashrate / 1e18)
	}
	format := float64Formatting(hash, 2, true)
	return fmt.Sprintf("%s.%s", format[0], format[1])
}

func simpleHashratePostfix(hashrate float64) string {
	if hashrate < 1e4 {
		return "H/s"
	}
	if hashrate < 1e6 {
		return "kH/s"
	} else if hashrate < 1e9 {
		return "MH/s"
	} else if hashrate < 1e12 {
		return "GH/s"
	} else if hashrate < 1e15 {
		return "TH/s"
	} else if hashrate < 1e18 {
		return "PH/s"
	} else {
		return "EH/s"
	}
}

func amountAsDecimalPartsTrimmed(v, numPlaces int64, useCommas bool) []string {
	// Filter numPlaces to only allow up to 8 decimal places trimming (eg. 1.12345678)
	if numPlaces > 8 {
		numPlaces = 8
	}

	// Separate values passed in into int.dec parts.
	intpart := v / 1e8
	decpart := v % 1e8

	// Format left side.
	left := strconv.FormatInt(intpart, 10)
	rightWithTail := fmt.Sprintf("%08d", decpart)

	// Reduce precision according to numPlaces.
	if len(rightWithTail) > int(numPlaces) {
		rightWithTail = rightWithTail[0:numPlaces]
	}

	// Separate trailing zeros.
	right := strings.TrimRight(rightWithTail, "0")
	tail := strings.TrimPrefix(rightWithTail, right)

	// Add commas (eg. 3,444.33)
	if useCommas && (len(left) > 3) {
		integerAsInt64, err := strconv.ParseInt(left, 10, 64)
		if err != nil {
			log.Errorf("amountAsDecimalParts comma formatting failed. Input: %v Error: %v", v, err.Error())
			left = "ERROR"
			right = "VALUE"
			tail = ""
			return []string{left, right, tail}
		}
		left = humanize.Comma(integerAsInt64)
	}

	return []string{left, right, tail}
}

// threeSigFigs returns a representation of the float formatted to three
// significant figures, with an appropriate magnitude prefix (k, M, B).
// For (k, M, G) prefixes for file/memory sizes, use humanize.Bytes.
func threeSigFigs(v float64) string {
	if v >= 1e11 {
		return fmt.Sprintf("%dB", int(math.Round(v/1e9)))
	} else if v >= 1e10 {
		return fmt.Sprintf("%.1fB", math.Round(v/1e8)/10)
	} else if v >= 1e9 {
		return fmt.Sprintf("%.2fB", math.Round(v/1e7)/1e2)
	} else if v >= 1e8 {
		return fmt.Sprintf("%dM", int(math.Round(v/1e6)))
	} else if v >= 1e7 {
		return fmt.Sprintf("%.1fM", math.Round(v/1e5)/10)
	} else if v >= 1e6 {
		return fmt.Sprintf("%.2fM", math.Round(v/1e4)/1e2)
	} else if v >= 1e5 {
		return fmt.Sprintf("%dk", int(math.Round(v/1e3)))
	} else if v >= 1e4 {
		return fmt.Sprintf("%.1fk", math.Round(v/100)/10)
	} else if v >= 1e3 {
		return fmt.Sprintf("%.2fk", math.Round(v/10)/1e2)
	} else if v >= 100 {
		return fmt.Sprintf("%d", int(math.Round(v)))
	} else if v >= 10 {
		return fmt.Sprintf("%.1f", math.Round(v*10)/10)
	} else if v >= 1 {
		return fmt.Sprintf("%.2f", math.Round(v*1e2)/1e2)
	} else if v >= 0.1 {
		return fmt.Sprintf("%.3f", math.Round(v*1e3)/1e3)
	} else if v >= 0.01 {
		return fmt.Sprintf("%.4f", math.Round(v*1e4)/1e4)
	} else if v >= 0.001 {
		return fmt.Sprintf("%.5f", math.Round(v*1e5)/1e5)
	} else if v >= 0.0001 {
		return fmt.Sprintf("%.6f", math.Round(v*1e6)/1e6)
	} else if v >= 0.00001 {
		return fmt.Sprintf("%.7f", math.Round(v*1e7)/1e7)
	} else if v == 0 {
		return "0"
	}
	return fmt.Sprintf("%.8f", math.Round(v*1e8)/1e8)
}

type periodMap struct {
	y          string
	mo         string
	d          string
	h          string
	min        string
	s          string
	sep        string
	pluralizer func(string, int) string
}

var shortPeriods = &periodMap{
	y:   "y",
	mo:  "mo",
	d:   "d",
	h:   "h",
	min: "m",
	s:   "s",
	sep: " ",
	pluralizer: func(s string, count int) string {
		return s
	},
}

var longPeriods = &periodMap{
	y:   " year",
	mo:  " month",
	d:   " day",
	h:   " hour",
	min: " minutes",
	s:   " seconds",
	sep: ", ",
	pluralizer: func(s string, count int) string {
		if count == 1 {
			return s
		}
		return s + "s"
	},
}

func formattedDuration(duration time.Duration, str *periodMap) string {
	durationyr := int(duration / (time.Hour * 24 * 365))
	durationmo := int((duration / (time.Hour * 24 * 30)) % 12)
	pl := str.pluralizer
	i := strconv.Itoa
	if durationyr != 0 {
		return i(durationyr) + "y " + i(durationmo) + "mo"
	}

	durationdays := int((duration / time.Hour / 24) % 30)
	if durationmo != 0 {
		return i(durationmo) + pl(str.mo, durationmo) + str.sep + i(durationdays) + pl(str.d, durationdays)
	}

	durationhr := int((duration / time.Hour) % 24)
	if durationdays != 0 {
		return i(durationdays) + pl(str.d, durationdays) + str.sep + i(durationhr) + pl(str.h, durationhr)
	}

	durationmin := int(duration.Minutes()) % 60
	if durationhr != 0 {
		return i(durationhr) + pl(str.h, durationhr) + str.sep + i(durationmin) + pl(str.min, durationmin)
	}

	durationsec := int(duration.Seconds()) % 60
	if (durationhr == 0) && (durationmin != 0) {
		return i(durationmin) + pl(str.min, durationmin) + str.sep + i(durationsec) + pl(str.s, durationsec)
	}
	return i(durationsec) + pl(str.s, durationsec)
}

func shortenString(s string, keep int) string {
	if len(s) <= 2*keep {
		return s
	}

	start := s[:keep]
	end := s[len(s)-keep:]

	return start + "..." + end
}

func makeTemplateFuncMap(params *chaincfg.Params) template.FuncMap {
	netTheme := "theme-" + strings.ToLower(netName(params))

	return template.FuncMap{
		"blockVoteBitsStr": func(voteBits uint16) string {
			if voteBits&1 == 0 {
				return "disapprove"
			}
			return "approve"
		},
		"txtypeStr": func(txtype int) string {
			return txhelpers.TxTypeToString(txtype)
		},
		"add": func(args ...int64) int64 {
			var sum int64
			for _, a := range args {
				sum += a
			}
			return sum
		},
		"intAdd": func(a, b int) int {
			return a + b
		},
		"subtract": func(a, b int64) int64 {
			return a - b
		},
		"floatsubtract": func(a, b float64) float64 {
			return a - b
		},
		"intSubtract": func(a, b int) int {
			return a - b
		},
		"divide": func(n, d int64) int64 {
			return n / d
		},
		"divideFloat": func(n, d float64) float64 {
			return n / d
		},
		"multiply": func(a, b int64) int64 {
			return a * b
		},
		"intMultiply": func(a, b int) int {
			return a * b
		},
		"floatMultiply": func(a, b float64) float64 {
			return a * b
		},
		"timezone": func() string {
			t, _ := time.Now().Zone()
			return t
		},
		"toint64": func(a int) int64 {
			return int64(a)
		},
		"timezoneOffset": func() int {
			_, secondsEastOfUTC := time.Now().Zone()
			return secondsEastOfUTC
		},
		"timeDurationShortString": func(timeInt int64) string {
			// to time
			input := time.Unix(timeInt, 0)
			now := time.Now()
			duration := now.Sub(input)
			return formattedDuration(duration, shortPeriods)
		},
		"percentage": func(a, b int64) float64 {
			return (float64(a) / float64(b)) * 100
		},
		"x100": func(v float64) float64 {
			return v * 100
		},
		"f32x100": func(v float32) float32 {
			return v * 100
		},
		"int64": toInt64,
		"intComma": func(v interface{}) string {
			return humanize.Comma(toInt64(v))
		},
		"rateCalculate": func(mainNum uint32, others ...uint32) float64 {
			total := mainNum
			for _, other := range others {
				total += other
			}
			return float64(mainNum) / float64(total)
		},
		"int64Comma":       humanize.Comma,
		"commaWithDecimal": humanize.CommafWithDigits,
		"ticketWindowProgress": func(i int) float64 {
			p := (float64(i) / float64(params.StakeDiffWindowSize)) * 100
			return p
		},
		"rewardAdjustmentProgress": func(i int) float64 {
			p := (float64(i) / float64(params.SubsidyReductionInterval)) * 100
			return p
		},
		"mutilchainRewardAdjustmentProgress": func(i int, subsidyInterval int64) float64 {
			p := (float64(i) / float64(subsidyInterval)) * 100
			return p
		},
		"float64AsDecimalParts": float64Formatting,
		"hashrateDecimalParts":  hashrateFormatting,
		"hashratePostfix":       hashratePostfix,
		"difficultyDisp":        difficultyDisp,
		"diffFormat":            diffFormat,
		"simpleHashrateDisp":    simpleHashrateDisp,
		"simpleHashratePostfix": simpleHashratePostfix,
		"normalFloat": func(n float64) string {
			return strconv.FormatFloat(n, 'f', 7, 64)
		},
		"normalWithPrecFloat": func(n float64, prc int) string {
			return strconv.FormatFloat(n, 'f', prc, 64)
		},
		"amountAsDecimalParts": func(v int64, useCommas bool) []string {
			return float64Formatting(dcrutil.Amount(v).ToCoin(), 8, useCommas)
		},
		"amountMulAsDecimalParts": func(v int64, useCommas bool, chainType string) []string {
			if chainType != mutilchain.TYPEXMR {
				return float64Formatting(dcrutil.Amount(v).ToCoin(), 8, useCommas)
			}
			return float64Formatting(utils.AtomicToXMR(uint64(v)), 8, useCommas)
		},
		"toFloat64Amount": func(intAmount int64) float64 {
			return dcrutil.Amount(intAmount).ToCoin()
		},
		"toMulFloat64Amount": func(intAmount int64, chainType string) float64 {
			switch chainType {
			case mutilchain.TYPEXMR:
				return float64(intAmount) / float64(utils.XmrAtomicUnit)
			default:
				return dcrutil.Amount(intAmount).ToCoin()
			}
		},
		"mulPowAlgorithm": func(chainType string) string {
			switch chainType {
			case mutilchain.TYPEBTC:
				return "SHA-256"
			case mutilchain.TYPELTC:
				return "Scrypt"
			case mutilchain.TYPEXMR:
				return "RandomX"
			default:
				return "N/A"
			}
		},
		"dcrPerKbToAtomsPerByte": func(amt dcrutil.Amount) int64 {
			return int64(math.Round(float64(amt) / 1e3))
		},
		"threeSigFigs": threeSigFigs,
		"remaining": func(idx int, max, t int64) string {
			x := (max - int64(idx)) * t
			if x == 0 {
				return "imminent"
			}
			allsecs := int(time.Duration(x).Seconds())
			str := ""
			// calculate years
			if allsecs > 31536000 {
				years := allsecs / 31536000
				allsecs %= 31536000
				str += fmt.Sprintf("%dy ", years)
			}
			// calculate months (Est)
			if allsecs > 2628000 {
				months := allsecs / 2628000
				allsecs %= 2628000
				str += fmt.Sprintf("%dmo ", months)
			}
			// calculate weeks
			if allsecs > 604799 {
				weeks := allsecs / 604800
				allsecs %= 604800
				str += fmt.Sprintf("%dw ", weeks)
			}
			// calculate day
			if allsecs > 86399 {
				days := allsecs / 86400
				allsecs %= 86400
				str += fmt.Sprintf("%dd ", days)
			}
			// calculate hours
			if allsecs > 3599 {
				hours := allsecs / 3600
				allsecs %= 3600
				str += fmt.Sprintf("%dh ", hours)
			}
			// calculate minutes
			if allsecs > 59 {
				mins := allsecs / 60
				allsecs %= 60
				str += fmt.Sprintf("%dm ", mins)
			}
			// calculate seconds
			if allsecs > 0 {
				str += fmt.Sprintf("%ds ", allsecs)
			}
			return str + "remaining"
		},
		"chainremaining": func(idx int, max, t int64) string {
			x := (max - int64(idx)) * t
			if x == 0 {
				return "imminent"
			}
			allsecs := int(time.Duration(x).Seconds())
			str := ""

			if allsecs > 604799 {
				weeks := allsecs / 604800
				allsecs %= 604800
				str += fmt.Sprintf("%dw ", weeks)
			}
			if allsecs > 86399 {
				days := allsecs / 86400
				allsecs %= 86400
				str += fmt.Sprintf("%dd ", days)
			}
			if allsecs > 3599 {
				hours := allsecs / 3600
				allsecs %= 3600
				str += fmt.Sprintf("%dh ", hours)
			}
			return str + "remaining"
		},
		"amountAsDecimalPartsTrimmed": amountAsDecimalPartsTrimmed,
		"secondsToLongDurationString": func(d int64) string {
			return formattedDuration(time.Duration(d)*time.Second, longPeriods)
		},
		"secondsToShortDurationString": func(d int64) string {
			return formattedDuration(time.Duration(d)*time.Second, shortPeriods)
		},
		"durationToShortDurationString": func(d time.Duration) string {
			return formattedDuration(d, shortPeriods)
		},
		"convertByteArrayToString": func(arr []byte) (inString string) {
			inString = hex.EncodeToString(arr)
			return
		},
		"uint16Mul": func(a uint16, b int) (result int) {
			result = int(a) * b
			return
		},
		"TimeConversion": func(a uint64) string {
			if a == 0 {
				return "N/A"
			}
			dateTime := time.Unix(int64(a), 0).UTC()
			return dateTime.Format("2006-01-02 15:04:05 MST")
		},
		"formatDateTime": func(t time.Time) string {
			d := time.Until(t)
			if d > 0 {
				return formattedDuration(d, shortPeriods)
			}
			return t.Format("2006-01-02 15:04:05 MST")
		},
		"dateTimeWithoutTimeZone": func(a int64) string {
			if a == 0 {
				return "N/A"
			}
			dateTime := time.Unix(int64(a), 0).UTC()
			return dateTime.Format("2006-01-02 15:04:05")
		},
		"dateFromInt": func(a uint64) string {
			if a == 0 {
				return "N/A"
			}
			dateTime := time.Unix(int64(a), 0).UTC()
			return dateTime.Format("2006-01-02")
		},
		"dateWithSlash": func(a uint64) string {
			if a == 0 {
				return "N/A"
			}
			dateTime := time.Unix(int64(a), 0).UTC()
			return dateTime.Format("2006/01/02")
		},
		"toLowerCase": strings.ToLower,
		"toUpperCase": strings.ToUpper,
		"chainName": func(chainType string) string {
			switch chainType {
			case mutilchain.TYPEBTC:
				return "Bitcoin"
			case mutilchain.TYPELTC:
				return "Litecoin"
			case mutilchain.TYPEXMR:
				return "Monero"
			default:
				return "Decred"
			}
		},
		"toTitleCase": titleCase,
		"xcDisplayName": func(token string) string {
			switch token {
			case "dcrdex":
				return "Dcrdex"
			}
			if exchanges.IsDCRBTCExchange(token) {
				return titleCase(exchanges.GetDCRBTCExchangeName(token))
			}
			return titleCase(token)
		},
		"xcLogoName": func(token string) string {
			if strings.HasPrefix(token, "btc_") {
				return strings.ReplaceAll(token, "btc_", "")
			}
			return token
		},
		"toDashWords": func(input string) string {
			return strings.ReplaceAll(input, " ", "-")
		},
		"prefixPath": func(prefix, path string) string {
			if path == "" {
				if strings.HasSuffix(prefix, "/") {
					return strings.TrimRight(prefix, "/") + "/"
				}
				return prefix
			}
			if prefix == "" {
				return path
			}
			return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(path, "/")
		},
		"fetchRowLinkURL": func(groupingStr string, endBlock int64, start, end time.Time) string {
			// fetchRowLinkURL creates links url to be used in the blocks list
			// views in hierarchical order i.e. /years -> /months -> weeks ->
			// /days -> /blocks (/years -> /months) simply means that on
			// "/years" page every row has a link to the "/months" page showing
			// the number of months that are expected to comprise a given row in
			// "/years" page i.e each row has a link like
			// "/months?offset=14&rows=12" with the offset unique for each row.
			var matchedGrouping string
			val := dbtypes.TimeGroupingFromStr(groupingStr)

			switch val {
			case dbtypes.YearGrouping:
				matchedGrouping = "months"

			case dbtypes.MonthGrouping:
				matchedGrouping = "weeks"

			case dbtypes.WeekGrouping:
				matchedGrouping = "days"

			// for dbtypes.DayGrouping and any other groupings default to blocks.
			default:
				matchedGrouping = "blocks"
			}

			matchingVal := dbtypes.TimeGroupingFromStr(matchedGrouping)
			intervalVal, err := dbtypes.TimeBasedGroupingToInterval(matchingVal)
			if err != nil {
				log.Debugf("Resolving the new group interval failed: error : %v", err)
				return fmt.Sprintf("/blocks?height=%d&rows=20", endBlock)
			}

			rowsCount := int64(end.Sub(start).Seconds()/intervalVal) + 1
			offset := int64(time.Since(end).Seconds() / intervalVal)

			if offset != 0 {
				offset++
			}

			return fmt.Sprintf("/%s?offset=%d&rows=%d",
				matchedGrouping, offset, rowsCount)
		},
		"theme": func() string {
			return netTheme
		},
		"uint16toInt64": func(v uint16) int64 {
			return int64(v)
		},
		"zeroSlice": func(length int) []int {
			if length < 0 {
				length = 0
			}
			return make([]int, length)
		},
		"clipSlice": func(arr []*types.TrimmedTxInfo, n int) []*types.TrimmedTxInfo {
			if len(arr) >= n {
				return arr[:n]
			}
			return arr
		},
		"hashlink": func(hash string, link string) [2]string {
			return [2]string{hash, link}
		},
		"hashStart": func(hash string) string {
			clipLen := 6
			hashLen := len(hash) - clipLen
			if hashLen < 1 {
				return ""
			}
			return hash[0:hashLen]
		},
		"hashEnd": func(hash string) string {
			clipLen := 6
			hashLen := len(hash) - clipLen
			if hashLen < 0 {
				return hash
			}
			return hash[hashLen:]
		},
		"redirectToMainnet": func(netName string, message string) bool {
			if netName != "Mainnet" && strings.Contains(message, "mainnet") {
				return true
			}
			return false
		},
		"redirectToTestnet": func(netName string, message string) bool {
			if netName != testnetNetName && strings.Contains(message, "testnet") {
				return true
			}
			return false
		},
		"toAbsValue": math.Abs,
		"toFloat64": func(x uint32) float64 {
			return float64(x)
		},
		"toInt": func(str string) int {
			intStr, err := strconv.Atoi(str)
			if err != nil {
				return 0
			}
			return intStr
		},
		"floor": math.Floor,
		"headData": func(data *CommonPageData, title string) headData {
			return headData{
				Data:  data,
				Title: title,
			}
		},
		"shortenHash": shortenString,
	}
}

type headData struct {
	Data  *CommonPageData
	Title string
}
