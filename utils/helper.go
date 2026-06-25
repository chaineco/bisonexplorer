package utils

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/decred/dcrdata/v8/mutilchain"
)

func ReadCsvFileFromUrl(filePath string) [][]string {
	resp, err := http.Get(filePath)
	if err != nil {
		fmt.Println("Unable to read input file for: ", filePath)
		return make([][]string, 0)
	}
	defer resp.Body.Close()
	csvReader := csv.NewReader(resp.Body)
	records, err := csvReader.ReadAll()
	if err != nil {
		fmt.Println("Unable to parse file as CSV for: ", filePath)
		return make([][]string, 0)
	}
	return records
}

func SumVolOfBwRow(row []string) float64 {
	sum := float64(0)
	for index, value := range row {
		if index == 0 {
			continue
		}
		floatValue, err := strconv.ParseFloat(value, 64)
		if err == nil {
			sum += floatValue
		}
	}
	return sum
}

func SumVolOfTimeRange(start time.Time, end time.Time, records [][]string) float64 {
	volSum := float64(0)
	startCal := false
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		dateRec, err := time.Parse("2006-01-02", record[0])
		if err != nil {
			continue
		}
		// if dateRec in range of date
		if ((start.Year() == dateRec.Year() && start.Month() == dateRec.Month() && start.Day() == dateRec.Day()) || dateRec.After(start)) &&
			((end.Year() == dateRec.Year() && end.Month() == dateRec.Month() && end.Day() == dateRec.Day()) || end.After(dateRec)) {
			if !startCal {
				startCal = true
			}
			volSum += SumVolOfBwRow(record)
		} else if startCal {
			break
		}
	}
	return volSum
}

func GroupByWeeklyData(records [][]string) [][]string {
	res := make([][]string, 0)
	curWeekData := make([]string, 0)
	curWeekDataNum := make([]float64, 0)
	var lastTime time.Time
	for _, record := range records {
		dateRec, err := time.Parse("2006-01-02", record[0])
		if err != nil {
			continue
		}
		weekDayNum := int(dateRec.Weekday())
		if len(curWeekDataNum) == 0 {
			for i := int(1); i < len(record); i++ {
				recordFloat, _ := strconv.ParseFloat(record[i], 64)
				curWeekDataNum = append(curWeekDataNum, recordFloat)
			}
		} else {
			for i := int(1); i < len(record); i++ {
				recordFloat, _ := strconv.ParseFloat(record[i], 64)
				curWeekDataNum[i-1] = curWeekDataNum[i-1] + recordFloat
			}
		}
		// if weekday is Monday
		if weekDayNum == 1 {
			curWeekData = append(curWeekData, record[0])
			sum := float64(0)
			for _, dataNum := range curWeekDataNum {
				curWeekData = append(curWeekData, fmt.Sprintf("%f", dataNum))
				sum += dataNum
			}
			curWeekData = append(curWeekData, fmt.Sprintf("%f", sum))
			res = append(res, curWeekData)
			curWeekData = make([]string, 0)
			curWeekDataNum = make([]float64, 0)
		}
		lastTime = dateRec
	}
	if len(curWeekDataNum) > 0 {
		// check nearest week
		for i := int(1); i < 7; i++ {
			nextDay := lastTime.AddDate(0, 0, i)
			if int(nextDay.Weekday()) == 1 {
				curWeekData = append(curWeekData, nextDay.Format("2006-01-02"))
				sum := float64(0)
				for _, dataNum := range curWeekDataNum {
					curWeekData = append(curWeekData, fmt.Sprintf("%f", dataNum))
					sum += dataNum
				}
				curWeekData = append(curWeekData, fmt.Sprintf("%f", sum))
				res = append(res, curWeekData)
				break
			}
		}
	}
	return res
}

func GroupByMonthlyData(records [][]string) [][]string {
	currentMonth := ""
	currentYear := ""
	res := make([][]string, 0)
	curMonthData := make([]string, 0)
	for index, record := range records {
		// date string
		dateArray := strings.Split(record[0], "-")
		if currentMonth == "" {
			currentYear = dateArray[0]
			currentMonth = dateArray[1]
			curMonthData = append(curMonthData, fmt.Sprintf("%s-%s", dateArray[0], dateArray[1]))
			for i := 1; i < len(record); i++ {
				curMonthData = append(curMonthData, record[i])
			}
		} else if dateArray[0] != currentYear || dateArray[1] != currentMonth {
			currentYear = dateArray[0]
			currentMonth = dateArray[1]
			res = append(res, curMonthData)
			curMonthData = make([]string, 0)
			curMonthData = append(curMonthData, fmt.Sprintf("%s-%s", dateArray[0], dateArray[1]))
			for i := 1; i < len(record); i++ {
				curMonthData = append(curMonthData, record[i])
			}
		} else {
			for i := 1; i < len(record); i++ {
				curFloat, _ := strconv.ParseFloat(curMonthData[i], 64)
				recordFloat, _ := strconv.ParseFloat(record[i], 64)
				curMonthData[i] = fmt.Sprintf("%f", curFloat+recordFloat)
			}
		}
		if index == len(records)-1 {
			res = append(res, curMonthData)
		}
	}
	for index, resItem := range res {
		sum := SumVolOfBwRow(resItem)
		resItem = append(resItem, fmt.Sprintf("%f", sum))
		res[index] = resItem
	}
	return res
}

func GetAgendaExtendInfo(agendaId string) []string {
	aDetail, exist := AgendasDetail[agendaId]
	if !exist {
		return []string{agendaId, ""}
	}
	return aDetail
}

func ReplaceDCP(input string) string {
	for dcp, link := range DCPLink {
		if strings.Contains(input, dcp) {
			input = strings.ReplaceAll(input, dcp, fmt.Sprintf("[[%s((%s))]]", dcp, link))
		}
	}
	return input
}

func GetSwapTypeDisplay(swapType string) string {
	switch swapType {
	case CONTRACT_TYPE:
		return "Swap Contract"
	case REDEMPTION_TYPE:
		return "Swap Redemption"
	case REFUND_TYPE:
		return "Swap Refund"
	}
	return ""
}

func GetSwapTypeFound(swapType string) string {
	switch swapType {
	case CONTRACT_TYPE:
		return "Contract"
	case REDEMPTION_TYPE:
		return "Redemption"
	case REFUND_TYPE:
		return "Refund"
	}
	return ""
}

func DateTimeWithoutTimeZone(timeInt int64) string {
	if timeInt == 0 {
		return "N/A"
	}
	dateTime := time.Unix(int64(timeInt), 0).UTC()
	return dateTime.Format("2006-01-02 15:04:05")
}

func IfaceToString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case *string:
		if x == nil {
			return ""
		}
		return *x
	case sql.NullString:
		if x.Valid {
			return x.String
		}
		return ""
	case *sql.NullString:
		if x != nil && x.Valid {
			return x.String
		}
		return ""
	case fmt.Stringer:
		return x.String()
	case *big.Int:
		return x.String()
	case big.Int:
		return x.String()
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// GetCirculatingSupply returns estimated circulating supply in atomic units (1 XMR = 1e12)
func GetCirculatingSupply(height uint64) uint64 {
	if height <= TailStartHeight {
		return InitialEmission
	}
	extraBlocks := height - TailStartHeight
	return InitialEmission + extraBlocks*TailEmissionPerBlock
}

func AtomicToXMR(atomic uint64) float64 {
	return float64(atomic) / float64(XmrAtomicUnit)
}

func XMRToAtomic(xmrAmount float64) int64 {
	return int64(math.Round(xmrAmount * XmrAtomicUnit))
}

func BTCToSatoshi(btcAmount float64) int64 {
	return int64(math.Round(btcAmount * BtcSatoshiUnit))
}

func SatoshiToBTC(satoshi int64) float64 {
	return float64(satoshi) / float64(BtcSatoshiUnit)
}

func MultichainAtomicToCoin(atomicAmount int64, chainType string) float64 {
	if chainType == mutilchain.TYPEXMR {
		return AtomicToXMR(uint64(atomicAmount))
	}
	return SatoshiToBTC(atomicAmount)
}

func GetCirculatingSupplyXMR(height uint64) float64 {
	return float64(GetCirculatingSupply(height)) / float64(XmrAtomicUnit)
}

func AvgOfArrayInt(array []int) int {
	if len(array) == 0 {
		return 0
	}
	sum := int(0)
	for _, item := range array {
		sum += item
	}
	res := int(math.Round(float64(sum) / float64(len(array))))
	return res
}

func IndexOf(slice []string, target string) int {
	for i, v := range slice {
		if v == target {
			return i
		}
	}
	return -1
}

func GetBlockchainName(chainType string) string {
	switch chainType {
	case mutilchain.TYPEBTC:
		return "Bitcoin"
	case mutilchain.TYPELTC:
		return "Litecoin"
	case mutilchain.TYPEXMR:
		return "Monero"
	case mutilchain.TYPEDCR:
		return "Decred"
	default:
		return "Unknown"
	}
}

// GetIPRange returns a normalized key used to group requests from the same
// origin for crawler/rate-limit tracking. It returns the full IP address so
// that throttling/blacklisting applies to a single client. Previously this
// collapsed IPv4 to its first two octets (a /16), which meant blacklisting a
// single abusive client also blocked up to ~65k unrelated users sharing that
// range (e.g. an entire consumer ISP block), producing site-wide blank pages.
func GetIPRange(s string) string {
	trimmed := strings.TrimSpace(s)
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return trimmed
	}
	return ip.String()
}
