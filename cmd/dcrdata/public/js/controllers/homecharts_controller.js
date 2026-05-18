import { Controller } from '@hotwired/stimulus'
import dompurify from 'dompurify'
import { assign, map, merge } from 'lodash-es'
import { animationFrame } from '../helpers/animation_helper.js'
import { isEqual } from '../helpers/chart_helper.js'
import { requestJSON } from '../helpers/http.js'
import humanize from '../helpers/humanize_helper.js'
import { getDefault } from '../helpers/module_helper.js'
import TurboQuery from '../helpers/turbolinks_helper.js'
import Zoom from '../helpers/zoom_helper.js'
import globalEventBus from '../services/event_bus_service.js'
import { darkEnabled } from '../services/theme_service.js'

let selectedChart
let selectedType
let Dygraph // lazy loaded on connect

const aDay = 86400 * 1000 // in milliseconds
const aMonth = 30 // in days
const atomsToDCR = 1e-8
const picoToXmr = 1e-12
const commonWindowScales = ['ticket-price', 'missed-votes']
const decredWindowScales = ['ticket-price', 'pow-difficulty', 'missed-votes']
let windowScales
const rangeUse = ['hashrate', 'pow-difficulty']
const hybridScales = ['privacy-participation']
const lineScales = ['ticket-price', 'privacy-participation', 'avg-age-days', 'coin-days-destroyed', 'coin-age-bands', 'mean-coin-age', 'total-coin-days', 'decoy-bands']
const modeScales = ['ticket-price']
const binDisabled = ['decoy-bands', 'coin-age-bands']
let multiYAxisChart = ['ticket-price', 'privacy-participation', 'avg-age-days', 'coin-days-destroyed', 'coin-age-bands', 'mean-coin-age', 'total-coin-days']
const decredMultiYAxisChart = ['ticket-price', 'coin-supply', 'privacy-participation', 'avg-age-days', 'coin-days-destroyed', 'coin-age-bands', 'mean-coin-age', 'total-coin-days']
const chainMultiYAxisChart = ['ticket-price', 'privacy-participation', 'avg-age-days', 'coin-days-destroyed', 'coin-age-bands', 'mean-coin-age', 'total-coin-days']
const coinAgeCharts = ['avg-age-days', 'coin-days-destroyed', 'coin-age-bands', 'mean-coin-age', 'total-coin-days']
const coinAgeBandsLabels = ['none', '>7Y', '5-7Y', '3-5Y', '2-3Y', '1-2Y', '6M-1Y', '1-6M', '1W-1M', '1D-1W', '<1D', 'Decred Price']
const coinAgeBandsColors = [
  '#e9baa6',
  '#152b83',
  '#dc3912',
  '#ff9900',
  '#109618',
  '#990099',
  '#0099c6',
  '#dd4477',
  '#66aa00',
  '#b82e2e',
  '#576812ff',
  '#4c4c4cff'
]
const decoyBandsLabels = ['none', 'No Tx', 'Decoys 0-3', 'Decoys 4-7', 'Decoys 8-11', 'Decoys 12-14', 'Decoys > 15', 'Mixin']
const decoyBandsColors = [
  '#e9baa6',
  '#576812ff',
  '#152b83',
  '#dc3912',
  '#ff9900',
  '#109618',
  '#990099',
  '#4c4c4cff'
]
const decredChartOpts = ['ticket-price', 'ticket-pool-size', 'ticket-pool-value', 'stake-participation',
  'privacy-participation', 'missed-votes', 'block-size', 'blockchain-size', 'tx-count', 'duration-btw-blocks',
  'pow-difficulty', 'chainwork', 'hashrate', 'coin-supply', 'fees', 'avg-age-days', 'coin-days-destroyed',
  'coin-age-bands', 'mean-coin-age', 'total-coin-days']
const mutilchainChartOpts = ['block-size', 'blockchain-size', 'tx-count', 'tx-per-block', 'address-number',
  'pow-difficulty', 'hashrate', 'mined-blocks', 'mempool-size', 'mempool-txs', 'coin-supply', 'fees']
const xmrChartOpts = ['block-size', 'blockchain-size', 'tx-count', 'tx-per-block',
  'pow-difficulty', 'hashrate', 'coin-supply', 'fees', 'duration-btw-blocks', 'avg-tx-size',
  'fee-rate', 'total-ring-size', 'avg-ring-size', 'decoy-bands']
let globalChainType = ''
// index 0 represents y1 and 1 represents y2 axes.
const yValueRanges = { 'ticket-price': [1] }
const hashrateUnits = ['H/s', 'KH/s', 'MH/s', 'GH/s', 'TH/s', 'PH/s', 'EH/s']
let ticketPoolSizeTarget, premine, stakeValHeight, stakeShare
let baseSubsidy, subsidyInterval, subsidyExponent, windowSize, avgBlockTime
let rawCoinSupply, rawPoolValue
let yFormatter, legendEntry, legendMarker, legendColorMaker, legendElement
let rangeOption = ''
const chainList = ['dcr', 'btc', 'ltc', 'xmr']
const targetTimePerBlock = [0, 0, 0, 0]
const yAxisLabelWidth = {
  y1: {
    'ticket-price': 40,
    'ticket-pool-size': 40,
    'ticket-pool-value': 40,
    'stake-participation': 40,
    'privacy-participation': 40,
    'missed-votes': 40,
    'block-size': 50,
    'blockchain-size': 50,
    'tx-count': 45,
    'duration-btw-blocks': 40,
    'pow-difficulty': 50,
    chainwork: 35,
    hashrate: 50,
    'coin-supply': 40,
    fees: 50,
    'tx-per-block': 50,
    'address-number': 45,
    'mined-blocks': 40,
    'mempool-size': 40,
    'mempool-txs': 50,
    'avg-age-days': 50,
    'coin-days-destroyed': 50,
    'coin-age-bands': 40,
    'mean-coin-age': 50,
    'total-coin-days': 50,
    'total-ring-size': 40,
    'avg-ring-size': 40,
    'fee-rate': 50,
    'avg-tx-size': 40,
    'decoy-bands': 40
  },
  y2: {
    'ticket-price': 45,
    'avg-age-days': 40,
    'coin-days-destroyed': 40,
    'coin-age-bands': 40,
    'mean-coin-age': 40,
    'total-coin-days': 40,
    'decoy-bands': 50
  }
}

function unitToCoin (chainType) {
  if (chainType === 'xmr') {
    return picoToXmr
  }
  return atomsToDCR
}

function missedVotesFunc (data) {
  if (data.t) return zipWindowTvY(data.t, data.missed)
  return zipWindowHvY(data.missed, data.window, 1, data.offset * data.window)
}

function formatYLegend (g, seriesName, yval, rowIdx, colIdx) {
  if (yval == null || isNaN(yval)) return '–'

  const props = g.getPropertiesForSeries(seriesName) // {axis:1|2, column, color,…}
  const axisKey = props.axis === 2 ? 'y2' : 'y'
  const axes = g.getOption('axes') || {}
  const axisOpts = axes[axisKey] || {}

  // Priority: valueFormatter of series → y axis → global
  const vf =
    g.getOption('valueFormatter', seriesName) ||
    axisOpts.valueFormatter ||
    g.getOption('valueFormatter')

  if (typeof vf === 'function') {
    // opts(name) like spec of Dygraphs
    const optsFn = (name) =>
      (axisOpts && axisOpts[name] !== undefined) ? axisOpts[name] : g.getOption(name)
    // Main signals: (num_or_millis, opts, seriesName, dygraph, row, col)
    return vf(yval, optsFn, seriesName, g, rowIdx, colIdx)
  }
  return String(yval)
}

function isMobile () {
  return window.innerWidth <= 768
}

function isCoinAgeChart (chart) {
  return coinAgeCharts.indexOf(chart) > -1
}

function hashrateTimePlotter (e) {
  // ASIC miners get turned on
  const asicLines = ['● ASIC miners', 'get turned on']
  hashrateLegendPlotter(e, 1546339611000, 197, asicLines, 'left')
  // Miner reward change 60% to 10%
  const minerLines = ['● Miner reward', 'change 60% to 10%']
  hashrateLegendPlotter(e, 1652266011000, 150, minerLines, 'right')
  if (rangeOption !== 'before' || rangeOption !== 'after') {
    const lines = ['● Transition of the', 'algorithm to BLAKE3', '● Miner reward', 'change 10% to 1%']
    hashrateLegendPlotter(e, 1693612800000, 0, lines, 'right')
  }
}

function hashrateBlockPlotter (e) {
  // ASIC miners get turned on
  const asicLines = ['● ASIC miners', 'get turned on']
  hashrateLegendPlotter(e, 306247, 197, asicLines, 'left')
  // Miner reward change 60% to 10%
  const minerLines = ['● Miner reward', 'change 60% to 10%']
  hashrateLegendPlotter(e, 658518, 150, minerLines, 'right')
  if (rangeOption !== 'before' || rangeOption !== 'after') {
    const lines = ['● Transition of the', 'algorithm to BLAKE3', '● Miner reward', 'change 10% to 1%']
    hashrateLegendPlotter(e, 794429, 0, lines, 'right')
  }
}

function difficultyTimePlotter (e) {
  // ASIC miners get turned on
  const asicLines = ['● ASIC miners', 'get turned on']
  hashrateLegendPlotter(e, 1546339611000, 11500000000, asicLines, 'left')
  // Miner reward change 60% to 10%
  const minerLines = ['● Miner reward', 'change 60% to 10%']
  hashrateLegendPlotter(e, 1652266011000, 10000000000, minerLines, 'right')
  if (rangeOption !== 'before' || rangeOption !== 'after') {
    const lines = ['● Transition of the', 'algorithm to BLAKE3', '● Miner reward', 'change 10% to 1%']
    hashrateLegendPlotter(e, 1693612800000, 0, lines, 'right')
  }
}

function difficultyBlockPlotter (e) {
  // ASIC miners get turned on
  const asicLines = ['● ASIC miners', 'get turned on']
  hashrateLegendPlotter(e, 306247, 11500000000, asicLines, 'left')
  // Miner reward change 60% to 10%
  const minerLines = ['● Miner reward', 'change 60% to 10%']
  hashrateLegendPlotter(e, 658518, 10000000000, minerLines, 'right')
  if (rangeOption !== 'before' || rangeOption !== 'after') {
    const lines = ['● Transition of the', 'algorithm to BLAKE3', '● Miner reward', 'change 10% to 1%']
    hashrateLegendPlotter(e, 794429, 0, lines, 'right')
  }
}

$(document).mouseup(function (e) {
  const selectArea = $('#chainChartSelectList')
  if (!selectArea.is(e.target) && selectArea.has(e.target).length === 0) {
    if (selectArea.css('display') !== 'none') {
      selectArea.css('display', 'none')
    }
  }
})

function makePt (x, y) { return { x, y } }

// Legend plotter processing for hashrate chart for blake3 algorithm transition
function hashrateLegendPlotter (e, midGapValue, startY, lines, direct) {
  Dygraph.Plotters.fillPlotter(e)
  Dygraph.Plotters.linePlotter(e)
  const area = e.plotArea
  const ctx = e.drawingContext
  const mg = e.dygraph.toDomCoords(midGapValue, startY)
  const midGap = makePt(mg[0], mg[1])
  const fontSize = 13
  const dark = darkEnabled()
  ctx.textAlign = 'left'
  ctx.textBaseline = 'top'
  ctx.font = `${fontSize}px arial`
  ctx.lineWidth = 1
  ctx.strokeStyle = dark ? '#ffffff' : '#259331'
  const boxColor = dark ? '#1e2b39' : '#ffffff'
  let boxW = 0
  const txts = [...lines]
  txts.forEach(txt => {
    const w = ctx.measureText(txt).width
    if (w > boxW) boxW = w
  })
  let rowHeight = fontSize * 1.5
  const rowPad = (rowHeight - fontSize) / 3
  const boxPad = rowHeight / 5
  let y = fontSize
  y += midGap.y - 2 * area.h / 3
  // Label the gap size.
  rowHeight -= 2 // just looks better
  ctx.fillStyle = boxColor
  const reactX = direct === 'left' ? midGap.x - boxW - boxPad * 2 : midGap.x + boxPad
  const rect = makePt(reactX, y - boxPad)
  const dims = makePt(boxW + boxPad * 3, rowHeight * lines.length + boxPad * 2)
  ctx.fillRect(rect.x, rect.y, dims.x, dims.y)
  ctx.strokeRect(rect.x, rect.y, dims.x, dims.y)
  ctx.fillStyle = dark ? '#ffffff' : '#259331'
  const centerX = direct === 'left' ? midGap.x - boxW / 2 - 4 : midGap.x + boxW / 2 + 8
  const write = s => {
    const cornerX = centerX - (ctx.measureText(s).width / 2)
    ctx.fillText(s, cornerX + rowPad, y + rowPad)
    y += rowHeight
  }

  ctx.save()
  lines.forEach((line) => {
    write(line)
  })
  // Draw a line from the box to the gap
  const lineToX = direct === 'left' ? midGap.x - boxW / 2 : midGap.x + boxW / 2
  drawLine(ctx,
    makePt(lineToX, y),
    makePt(midGap.x, midGap.y - boxPad))
}

function drawLine (ctx, start, end) {
  ctx.beginPath()
  ctx.moveTo(start.x, start.y)
  ctx.lineTo(end.x, end.y)
  ctx.stroke()
}

function useRange (chart) {
  return rangeUse.indexOf(chart) > -1
}

function usesWindowUnits (chart) {
  return windowScales.indexOf(chart) > -1
}

function usesHybridUnits (chart) {
  return hybridScales.indexOf(chart) > -1
}

function isScaleDisabled (chart) {
  return lineScales.indexOf(chart) > -1
}

function isBinDisabled (chart) {
  return binDisabled.indexOf(chart) > -1
}

function isModeEnabled (chart) {
  return modeScales.includes(chart)
}

function hasMultipleVisibility (chart) {
  return multiYAxisChart.indexOf(chart) > -1
}

function intComma (amount) {
  if (!amount) return ''
  return amount.toLocaleString(undefined, { maximumFractionDigits: 0 })
}

function zipWindowHvYZ (ys, zs, winSize, yMult, zMult, offset) {
  yMult = yMult || 1
  zMult = zMult || 1
  offset = offset || 0
  return ys.map((y, i) => {
    return [i * winSize + offset, y * yMult, zs[i] * zMult]
  })
}

function zipWindowTvYZ (times, ys, zs, yMult, zMult) {
  yMult = yMult || 1
  zMult = zMult || 1
  return times.map((t, i) => {
    return [new Date(t * 1000), ys[i] * yMult, zs[i] * zMult]
  })
}

function ticketPriceFunc (data) {
  if (data.t) return zipWindowTvYZ(data.t, data.price, data.count, atomsToDCR)
  return zipWindowHvYZ(data.price, data.count, data.window, atomsToDCR)
}

function poolSizeFunc (data) {
  let out = []
  if (data.axis === 'height') {
    if (data.bin === 'block') out = zipIvY(data.count)
    else out = zipHvY(data.h, data.count)
  } else {
    out = zipTvY(data.t, data.count)
  }
  out.forEach(pt => pt.push(null))
  if (out.length) {
    out[0][2] = ticketPoolSizeTarget
    out[out.length - 1][2] = ticketPoolSizeTarget
  }
  return out
}

function percentStakedFunc (data) {
  rawCoinSupply = data.circulation.map(v => v * atomsToDCR)
  rawPoolValue = data.poolval.map(v => v * atomsToDCR)
  const ys = rawPoolValue.map((v, i) => [v / rawCoinSupply[i] * 100])
  if (data.axis === 'height') {
    if (data.bin === 'block') return zipIvY(ys)
    return zipHvY(data.h, ys)
  }
  return zipTvY(data.t, ys)
}

function anonymitySetFunc (data) {
  let d
  let start = -1
  let end = 0
  if (data.axis === 'height') {
    if (data.bin === 'block') {
      d = data.anonymitySet.map((y, i) => {
        if (start === -1 && y > 0) {
          start = i
        }
        end = i
        return [i, y * atomsToDCR]
      })
    } else {
      d = data.anonymitySet.map((y, i) => {
        if (start === -1 && y > 0) {
          start = i
        }
        end = data.h[i]
        return [data.h[i], y * atomsToDCR]
      })
    }
  } else {
    d = data.t.map((t, i) => {
      if (start === -1 && data.anonymitySet[i] > 0) {
        start = t * 1000
      }
      end = t * 1000
      return [new Date(t * 1000), data.anonymitySet[i] * atomsToDCR]
    })
  }
  return { data: d, limits: [start, end] }
}

function axesToRestoreYRange (chartName, origYRange, newYRange) {
  const axesIndexes = yValueRanges[chartName]
  if (!Array.isArray(origYRange) || !Array.isArray(newYRange) ||
    origYRange.length !== newYRange.length || !axesIndexes) return

  let axes
  for (let i = 0; i < axesIndexes.length; i++) {
    const index = axesIndexes[i]
    if (newYRange.length <= index) continue
    if (!isEqual(origYRange[index], newYRange[index])) {
      if (!axes) axes = {}
      if (index === 0) {
        axes = Object.assign(axes, { y1: { valueRange: origYRange[index] } })
      } else if (index === 1) {
        axes = Object.assign(axes, { y2: { valueRange: origYRange[index] } })
      }
    }
  }
  return axes
}

function withBigUnits (v, units) {
  const i = v === 0 ? 0 : Math.floor(Math.log10(v) / 3)
  return (v / Math.pow(1000, i)).toFixed(3) + ' ' + units[i]
}

function blockReward (height) {
  if (height >= stakeValHeight) return baseSubsidy * Math.pow(subsidyExponent, Math.floor(height / subsidyInterval))
  if (height > 1) return baseSubsidy * (1 - stakeShare)
  if (height === 1) return premine
  return 0
}

function addLegendEntryFmt (div, series, fmt) {
  div.appendChild(legendEntry(`${series.dashHTML} ${series.labelHTML}: ${fmt(series.y)}`))
}

function addLegendEntry (div, series) {
  div.appendChild(legendEntry(`${series.dashHTML} ${series.labelHTML}: ${series.yHTML}`))
}

function defaultYFormatter (div, data) {
  if (!data.series || data.series.length === 0) return
  data.series.forEach(s => {
    if (s.y == null || isNaN(s.y)) return
    addLegendEntry(div, s) // default: show raw value
  })
}

function customYFormatter (fmt) {
  return (div, data) => {
    if (!data.series || data.series.length === 0) return
    data.series.forEach(s => {
      if (s.y == null || isNaN(s.y)) return
      addLegendEntryFmt(div, s, y => fmt(s.y))
    })
  }
}

function legendFormatter (data) {
  if (data.x == null) return legendElement.classList.add('d-hide')
  legendElement.classList.remove('d-hide')
  const div = document.createElement('div')
  let xHTML = data.xHTML
  if (data.dygraph.getLabels()[0] === 'Date') {
    xHTML = humanize.date(data.x, false, selectedChart !== 'ticket-price')
  }
  div.appendChild(legendEntry(`${data.dygraph.getLabels()[0]}: ${xHTML}`))
  yFormatter(div, data, data.dygraph.getOption('legendIndex'))
  dompurify.sanitize(div, { IN_PLACE: true, FORBID_TAGS: ['svg', 'math'] })
  return div.innerHTML
}

function nightModeOptions (nightModeOn) {
  if (nightModeOn) {
    return {
      rangeSelectorAlpha: 0.3,
      gridLineColor: '#596D81',
      colors: ['#2DD8A3', '#255595', '#FFC84E']
    }
  }
  return {
    rangeSelectorAlpha: 0.4,
    gridLineColor: '#C4CBD2',
    colors: ['#255594', '#006600', '#FF0090']
  }
}

// data coin age handler for bin = day, axis height
function zipCoinAgeHvY (heights, ys, zs, yMult, zMult, offset) {
  yMult = yMult || 1
  zMult = zMult || 1
  offset = offset || 1
  return ys.map((y, i) => {
    return [offset + heights[i], y * yMult, zs[i] * zMult]
  })
}

// data coin age handler for axis time
function zipCoinAgeTvY (times, ys, zs, yMult, zMult) {
  yMult = yMult || 1
  zMult = zMult || 1
  return times.map((t, i) => {
    return [new Date(t * 1000), ys[i] * yMult, zs[i] * zMult]
  })
}

// data coin age handler for bin = block, axis height
function zipCoinAgeIvY (ys, zs, yMult, zMult, offset) {
  yMult = yMult || 1
  zMult = zMult || 1
  offset = offset || 1 // TODO: check for why offset is set to a default value of 1 when genesis block has a height of 0
  return ys.map((y, i) => {
    return [offset + i, y * yMult, zs[i] * zMult]
  })
}

// data coin age bands handler for bin = day, axis height
function zipCoinAgeBandsHvY (heights, ys, zs, yMult, zMult, offset) {
  yMult = yMult || 1
  zMult = zMult || 1
  offset = offset || 1
  return ys.map((y, i) => {
    const less1Day = Number(y.less1Day)
    const dayToWeek = Number(y.dayToWeek)
    const weekToMonth = Number(y.weekToMonth)
    const monthToHalfYear = Number(y.monthToHalfYear)
    const halfYearToYear = Number(y.halfYearToYear)
    const yearTo2Year = Number(y.yearTo2Year)
    const twoYearTo3Year = Number(y.twoYearTo3Year)
    const threeYearTo5Year = Number(y.threeYearTo5Year)
    const fiveYearTo7Year = Number(y.fiveYearTo7Year)
    const greaterThan7Year = Number(y.greaterThan7Year)
    const totalValue = less1Day + dayToWeek + weekToMonth + monthToHalfYear + halfYearToYear + yearTo2Year +
      twoYearTo3Year + threeYearTo5Year + fiveYearTo7Year + greaterThan7Year
    // const less1DayPercent = (less1Day / totalValue) * 100
    const dayToWeekPercent = (dayToWeek / totalValue) * 100 + 0.00001
    const weekToMonthPercent = (weekToMonth / totalValue) * 100 + 0.00001
    const monthToHalfYearPercent = (monthToHalfYear / totalValue) * 100 + 0.00001
    const halfYearToYearPercent = (halfYearToYear / totalValue) * 100 + 0.00001
    const yearTo2YearPercent = (yearTo2Year / totalValue) * 100 + 0.00001
    const twoYearTo3YearPercent = (twoYearTo3Year / totalValue) * 100 + 0.00001
    const threeYearTo5YearPercent = (threeYearTo5Year / totalValue) * 100 + 0.00001
    const fiveYearTo7YearPercent = (fiveYearTo7Year / totalValue) * 100 + 0.00001
    const greaterThan7YearPercent = (greaterThan7Year / totalValue) * 100 + 0.00001
    const noneValue = 0.00001
    const less1DayPercent = 100 - dayToWeekPercent - weekToMonthPercent - monthToHalfYearPercent - halfYearToYearPercent -
      yearTo2YearPercent - twoYearTo3YearPercent - threeYearTo5YearPercent - fiveYearTo7YearPercent - greaterThan7YearPercent - noneValue
    return [
      offset + heights[i],
      noneValue,
      greaterThan7YearPercent * yMult,
      fiveYearTo7YearPercent * yMult,
      threeYearTo5YearPercent * yMult,
      twoYearTo3YearPercent * yMult,
      yearTo2YearPercent * yMult,
      halfYearToYearPercent * yMult,
      monthToHalfYearPercent * yMult,
      weekToMonthPercent * yMult,
      dayToWeekPercent * yMult,
      less1DayPercent * yMult,
      zs[i] * zMult]
  })
}

// data coin age bands handler for bin = block, axis height
function zipCoinAgeBandsIvY (ys, zs, yMult, zMult, offset) {
  yMult = yMult || 1
  zMult = zMult || 1
  offset = offset || 1 // TODO: check for why offset is set to a default value of 1 when genesis block has a height of 0
  return ys.map((y, i) => {
    const less1Day = Number(y.less1Day)
    const dayToWeek = Number(y.dayToWeek)
    const weekToMonth = Number(y.weekToMonth)
    const monthToHalfYear = Number(y.monthToHalfYear)
    const halfYearToYear = Number(y.halfYearToYear)
    const yearTo2Year = Number(y.yearTo2Year)
    const twoYearTo3Year = Number(y.twoYearTo3Year)
    const threeYearTo5Year = Number(y.threeYearTo5Year)
    const fiveYearTo7Year = Number(y.fiveYearTo7Year)
    const greaterThan7Year = Number(y.greaterThan7Year)
    const totalValue = less1Day + dayToWeek + weekToMonth + monthToHalfYear + halfYearToYear + yearTo2Year +
      twoYearTo3Year + threeYearTo5Year + fiveYearTo7Year + greaterThan7Year
    // const less1DayPercent = (less1Day / totalValue) * 100
    const dayToWeekPercent = (dayToWeek / totalValue) * 100 + 0.00001
    const weekToMonthPercent = (weekToMonth / totalValue) * 100 + 0.00001
    const monthToHalfYearPercent = (monthToHalfYear / totalValue) * 100 + 0.00001
    const halfYearToYearPercent = (halfYearToYear / totalValue) * 100 + 0.00001
    const yearTo2YearPercent = (yearTo2Year / totalValue) * 100 + 0.00001
    const twoYearTo3YearPercent = (twoYearTo3Year / totalValue) * 100 + 0.00001
    const threeYearTo5YearPercent = (threeYearTo5Year / totalValue) * 100 + 0.00001
    const fiveYearTo7YearPercent = (fiveYearTo7Year / totalValue) * 100 + 0.00001
    const greaterThan7YearPercent = (greaterThan7Year / totalValue) * 100 + 0.00001
    const noneValue = 0.00001
    const less1DayPercent = 100 - dayToWeekPercent - weekToMonthPercent - monthToHalfYearPercent - halfYearToYearPercent -
      yearTo2YearPercent - twoYearTo3YearPercent - threeYearTo5YearPercent - fiveYearTo7YearPercent - greaterThan7YearPercent - noneValue
    return [
      offset + i,
      noneValue,
      greaterThan7YearPercent * yMult,
      fiveYearTo7YearPercent * yMult,
      threeYearTo5YearPercent * yMult,
      twoYearTo3YearPercent * yMult,
      yearTo2YearPercent * yMult,
      halfYearToYearPercent * yMult,
      monthToHalfYearPercent * yMult,
      weekToMonthPercent * yMult,
      dayToWeekPercent * yMult,
      less1DayPercent * yMult,
      zs[i] * zMult]
  })
}

// data coin age bands handler for axis time
function zipCoinAgeBandsTvY (times, ys, zs, yMult, zMult) {
  yMult = yMult || 1
  zMult = zMult || 1
  return times.map((t, i) => {
    const y = ys[i]
    const less1Day = Number(y.less1Day)
    const dayToWeek = Number(y.dayToWeek)
    const weekToMonth = Number(y.weekToMonth)
    const monthToHalfYear = Number(y.monthToHalfYear)
    const halfYearToYear = Number(y.halfYearToYear)
    const yearTo2Year = Number(y.yearTo2Year)
    const twoYearTo3Year = Number(y.twoYearTo3Year)
    const threeYearTo5Year = Number(y.threeYearTo5Year)
    const fiveYearTo7Year = Number(y.fiveYearTo7Year)
    const greaterThan7Year = Number(y.greaterThan7Year)
    const totalValue = less1Day + dayToWeek + weekToMonth + monthToHalfYear + halfYearToYear + yearTo2Year +
      twoYearTo3Year + threeYearTo5Year + fiveYearTo7Year + greaterThan7Year
    // const less1DayPercent = (less1Day / totalValue) * 100
    const dayToWeekPercent = (dayToWeek / totalValue) * 100 + 0.00001
    const weekToMonthPercent = (weekToMonth / totalValue) * 100 + 0.00001
    const monthToHalfYearPercent = (monthToHalfYear / totalValue) * 100 + 0.00001
    const halfYearToYearPercent = (halfYearToYear / totalValue) * 100 + 0.00001
    const yearTo2YearPercent = (yearTo2Year / totalValue) * 100 + 0.00001
    const twoYearTo3YearPercent = (twoYearTo3Year / totalValue) * 100 + 0.00001
    const threeYearTo5YearPercent = (threeYearTo5Year / totalValue) * 100 + 0.00001
    const fiveYearTo7YearPercent = (fiveYearTo7Year / totalValue) * 100 + 0.00001
    const greaterThan7YearPercent = (greaterThan7Year / totalValue) * 100 + 0.00001
    const noneValue = 0.00001
    const less1DayPercent = 100 - dayToWeekPercent - weekToMonthPercent - monthToHalfYearPercent - halfYearToYearPercent -
      yearTo2YearPercent - twoYearTo3YearPercent - threeYearTo5YearPercent - fiveYearTo7YearPercent - greaterThan7YearPercent - noneValue
    return [
      new Date(t * 1000),
      noneValue,
      greaterThan7YearPercent * yMult,
      fiveYearTo7YearPercent * yMult,
      threeYearTo5YearPercent * yMult,
      twoYearTo3YearPercent * yMult,
      yearTo2YearPercent * yMult,
      halfYearToYearPercent * yMult,
      monthToHalfYearPercent * yMult,
      weekToMonthPercent * yMult,
      dayToWeekPercent * yMult,
      less1DayPercent * yMult,
      zs[i] * zMult]
  })
}

// handler func fo avg age days
function avgAgeDaysFunc (data) {
  if (data.axis === 'height') {
    if (data.bin === 'block') {
      return zipCoinAgeIvY(data.avgAge, data.marketPrice)
    } else {
      return zipCoinAgeHvY(data.h, data.avgAge, data.marketPrice)
    }
  } else {
    return zipCoinAgeTvY(data.t, data.avgAge, data.marketPrice)
  }
}

// handler func for coin day destroyed
function avgCoinDayDestroyedFunc (data) {
  if (data.axis === 'height') {
    if (data.bin === 'block') {
      return zipCoinAgeIvY(data.cdd, data.marketPrice)
    } else {
      return zipCoinAgeHvY(data.h, data.cdd, data.marketPrice)
    }
  } else {
    return zipCoinAgeTvY(data.t, data.cdd, data.marketPrice)
  }
}

// handler func for coin age bands
function coindAgeBandsFunc (data) {
  if (data.axis === 'height') {
    if (data.bin === 'block') {
      return zipCoinAgeBandsIvY(data.ageBands, data.marketPrice)
    } else {
      return zipCoinAgeBandsHvY(data.h, data.ageBands, data.marketPrice)
    }
  } else {
    return zipCoinAgeBandsTvY(data.t, data.ageBands, data.marketPrice)
  }
}

// handler func for mean coin age
function meanCoinAgeFunc (data) {
  if (data.axis === 'height') {
    if (data.bin === 'block') {
      return zipCoinAgeIvY(data.meanCoinAge, data.marketPrice)
    } else {
      return zipCoinAgeHvY(data.h, data.meanCoinAge, data.marketPrice)
    }
  } else {
    return zipCoinAgeTvY(data.t, data.meanCoinAge, data.marketPrice)
  }
}

// handler func for total coin days (SUM coin age)
function totalCoinDaysFunc (data) {
  if (data.axis === 'height') {
    if (data.bin === 'block') {
      return zipCoinAgeIvY(data.totalCoinDays, data.marketPrice)
    } else {
      return zipCoinAgeHvY(data.h, data.totalCoinDays, data.marketPrice)
    }
  } else {
    return zipCoinAgeTvY(data.t, data.totalCoinDays, data.marketPrice)
  }
}

function zipWindowHvY (ys, winSize, yMult, offset) {
  yMult = yMult || 1
  offset = offset || 0
  return ys.map((y, i) => {
    return [i * winSize + offset, y * yMult]
  })
}

function zipWindowTvY (times, ys, yMult) {
  yMult = yMult || 1
  return times.map((t, i) => {
    return [new Date(t * 1000), ys[i] * yMult]
  })
}

function zipTvY (times, ys, yMult) {
  yMult = yMult || 1
  return times.map((t, i) => {
    return [new Date(t * 1000), ys[i] * yMult]
  })
}

function zipIvY (ys, yMult, offset) {
  yMult = yMult || 1
  offset = offset || 1 // TODO: check for why offset is set to a default value of 1 when genesis block has a height of 0
  return ys.map((y, i) => {
    return [offset + i, y * yMult]
  })
}

function zipHvY (heights, ys, yMult, offset) {
  yMult = yMult || 1
  offset = offset || 1
  return ys.map((y, i) => {
    return [offset + heights[i], y * yMult]
  })
}

function zip2D (data, ys, yMult, offset) {
  yMult = yMult || 1
  if (data.axis === 'height') {
    if (data.bin === 'block') return zipIvY(ys, yMult)
    return zipHvY(data.h, ys, yMult, offset)
  }
  return zipTvY(data.t, ys, yMult)
}

function decoyBandsFunc (data) {
  if (data.axis === 'height') {
    if (data.bin === 'block') {
      return zipDecoyBandsIvY(data.decoy, data.ringSize)
    } else {
      return zipDecoyBandsHvY(data.h, data.decoy, data.ringSize)
    }
  } else {
    return zipDecoyBandsTvY(data.t, data.decoy, data.ringSize)
  }
}

function zipDecoyBandsTvY (times, ys, zs, yMult, zMult) {
  yMult = yMult || 1
  zMult = zMult || 1
  return times.map((t, i) => {
    const y = ys[i]
    let decoyNoTx = Number(y.noTx)
    let decoy03Percent = Number(y.decoy03)
    let decoy47Percent = Number(y.decoy47)
    let decoy811Percent = Number(y.decoy811)
    let decoy1214Percent = Number(y.decoy1214)
    let decoyGe15Percent = Number(y.decoyGt15)

    if (decoyNoTx < 100) {
      decoyNoTx += 0.00001
    }
    if (decoy03Percent < 100) {
      decoy03Percent += 0.00001
    }
    if (decoy47Percent < 100) {
      decoy47Percent += 0.00001
    }
    if (decoy811Percent < 100) {
      decoy811Percent += 0.00001
    }
    if (decoy1214Percent < 100) {
      decoy1214Percent += 0.00001
    }
    if (decoyGe15Percent < 100) {
      decoyGe15Percent += 0.00001
    }
    const noneValue = 0.00001

    if (decoy03Percent === 100) {
      decoy03Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
    }

    if (decoy47Percent === 100) {
      decoy47Percent = 100 - decoyNoTx - decoy03Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
    }

    if (decoy811Percent === 100) {
      decoy811Percent = 100 - decoyNoTx - decoy47Percent - decoy03Percent - decoy1214Percent - decoyGe15Percent - noneValue
    }

    if (decoy1214Percent === 100) {
      decoy1214Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy03Percent - decoyGe15Percent - noneValue
    }

    if (decoyGe15Percent === 100) {
      decoyGe15Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - noneValue
    }

    if (decoyNoTx === 100) {
      decoyNoTx = 100 - decoyGe15Percent - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - noneValue
    }

    if (decoyNoTx + decoy03Percent + decoy47Percent + decoy811Percent + decoy1214Percent + decoyGe15Percent + noneValue > 100) {
      if (decoy03Percent > 0.1) {
        decoy03Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
      } else {
        if (decoy47Percent > 0.1) {
          decoy47Percent = 100 - decoyNoTx - decoy03Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
        } else {
          if (decoy811Percent > 0.1) {
            decoy811Percent = 100 - decoyNoTx - decoy47Percent - decoy03Percent - decoy1214Percent - decoyGe15Percent - noneValue
          } else {
            if (decoy1214Percent > 0.1) {
              decoy1214Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy03Percent - decoyGe15Percent - noneValue
            } else {
              if (decoyGe15Percent > 0.1) {
                decoyGe15Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - noneValue
              } else if (decoyNoTx > 0.1) {
                decoyNoTx = 100 - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - decoyGe15Percent - noneValue
              }
            }
          }
        }
      }
    }

    return [
      new Date(t * 1000),
      noneValue,
      decoyNoTx * yMult,
      decoy03Percent * yMult,
      decoy47Percent * yMult,
      decoy811Percent * yMult,
      decoy1214Percent * yMult,
      decoyGe15Percent * yMult,
      zs[i] * zMult]
  })
}

function zipDecoyBandsHvY (heights, ys, zs, yMult, zMult, offset) {
  yMult = yMult || 1
  zMult = zMult || 1
  offset = offset || 1
  return ys.map((y, i) => {
    let decoyNoTx = Number(y.noTx)
    let decoy03Percent = Number(y.decoy03)
    let decoy47Percent = Number(y.decoy47)
    let decoy811Percent = Number(y.decoy811)
    let decoy1214Percent = Number(y.decoy1214)
    let decoyGe15Percent = Number(y.decoyGt15)

    if (decoyNoTx < 100) {
      decoyNoTx += 0.00001
    }
    if (decoy03Percent < 100) {
      decoy03Percent += 0.00001
    }
    if (decoy47Percent < 100) {
      decoy47Percent += 0.00001
    }
    if (decoy811Percent < 100) {
      decoy811Percent += 0.00001
    }
    if (decoy1214Percent < 100) {
      decoy1214Percent += 0.00001
    }
    if (decoyGe15Percent < 100) {
      decoyGe15Percent += 0.00001
    }
    const noneValue = 0.00001

    if (decoy03Percent === 100) {
      decoy03Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
    }

    if (decoy47Percent === 100) {
      decoy47Percent = 100 - decoyNoTx - decoy03Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
    }

    if (decoy811Percent === 100) {
      decoy811Percent = 100 - decoyNoTx - decoy47Percent - decoy03Percent - decoy1214Percent - decoyGe15Percent - noneValue
    }

    if (decoy1214Percent === 100) {
      decoy1214Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy03Percent - decoyGe15Percent - noneValue
    }

    if (decoyGe15Percent === 100) {
      decoyGe15Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - noneValue
    }

    if (decoyNoTx === 100) {
      decoyNoTx = 100 - decoyGe15Percent - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - noneValue
    }

    if (decoyNoTx + decoy03Percent + decoy47Percent + decoy811Percent + decoy1214Percent + decoyGe15Percent + noneValue > 100) {
      if (decoy03Percent > 0.1) {
        decoy03Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
      } else {
        if (decoy47Percent > 0.1) {
          decoy47Percent = 100 - decoyNoTx - decoy03Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
        } else {
          if (decoy811Percent > 0.1) {
            decoy811Percent = 100 - decoyNoTx - decoy47Percent - decoy03Percent - decoy1214Percent - decoyGe15Percent - noneValue
          } else {
            if (decoy1214Percent > 0.1) {
              decoy1214Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy03Percent - decoyGe15Percent - noneValue
            } else {
              if (decoyGe15Percent > 0.1) {
                decoyGe15Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - noneValue
              } else if (decoyNoTx > 0.1) {
                decoyNoTx = 100 - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - decoyGe15Percent - noneValue
              }
            }
          }
        }
      }
    }

    return [
      offset + heights[i],
      noneValue,
      decoyNoTx * yMult,
      decoy03Percent * yMult,
      decoy47Percent * yMult,
      decoy811Percent * yMult,
      decoy1214Percent * yMult,
      decoyGe15Percent * yMult,
      zs[i] * zMult]
  })
}

function zipDecoyBandsIvY (ys, zs, yMult, zMult, offset) {
  yMult = yMult || 1
  zMult = zMult || 1
  offset = offset || 1
  return ys.map((y, i) => {
    let decoyNoTx = Number(y.noTx)
    let decoy03Percent = Number(y.decoy03)
    let decoy47Percent = Number(y.decoy47)
    let decoy811Percent = Number(y.decoy811)
    let decoy1214Percent = Number(y.decoy1214)
    let decoyGe15Percent = Number(y.decoyGt15)

    if (decoyNoTx < 100) {
      decoyNoTx += 0.00001
    }
    if (decoy03Percent < 100) {
      decoy03Percent += 0.00001
    }
    if (decoy47Percent < 100) {
      decoy47Percent += 0.00001
    }
    if (decoy811Percent < 100) {
      decoy811Percent += 0.00001
    }
    if (decoy1214Percent < 100) {
      decoy1214Percent += 0.00001
    }
    if (decoyGe15Percent < 100) {
      decoyGe15Percent += 0.00001
    }
    const noneValue = 0.00001

    if (decoy03Percent === 100) {
      decoy03Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
    }

    if (decoy47Percent === 100) {
      decoy47Percent = 100 - decoyNoTx - decoy03Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
    }

    if (decoy811Percent === 100) {
      decoy811Percent = 100 - decoyNoTx - decoy47Percent - decoy03Percent - decoy1214Percent - decoyGe15Percent - noneValue
    }

    if (decoy1214Percent === 100) {
      decoy1214Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy03Percent - decoyGe15Percent - noneValue
    }

    if (decoyGe15Percent === 100) {
      decoyGe15Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - noneValue
    }

    if (decoyNoTx === 100) {
      decoyNoTx = 100 - decoyGe15Percent - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - noneValue
    }

    if (decoyNoTx + decoy03Percent + decoy47Percent + decoy811Percent + decoy1214Percent + decoyGe15Percent + noneValue > 100) {
      if (decoy03Percent > 0.1) {
        decoy03Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
      } else {
        if (decoy47Percent > 0.1) {
          decoy47Percent = 100 - decoyNoTx - decoy03Percent - decoy811Percent - decoy1214Percent - decoyGe15Percent - noneValue
        } else {
          if (decoy811Percent > 0.1) {
            decoy811Percent = 100 - decoyNoTx - decoy47Percent - decoy03Percent - decoy1214Percent - decoyGe15Percent - noneValue
          } else {
            if (decoy1214Percent > 0.1) {
              decoy1214Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy03Percent - decoyGe15Percent - noneValue
            } else {
              if (decoyGe15Percent > 0.1) {
                decoyGe15Percent = 100 - decoyNoTx - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - noneValue
              } else if (decoyNoTx > 0.1) {
                decoyNoTx = 100 - decoy47Percent - decoy811Percent - decoy1214Percent - decoy03Percent - decoyGe15Percent - noneValue
              }
            }
          }
        }
      }
    }

    return [
      offset + i,
      noneValue,
      decoyNoTx * yMult,
      decoy03Percent * yMult,
      decoy47Percent * yMult,
      decoy811Percent * yMult,
      decoy1214Percent * yMult,
      decoyGe15Percent * yMult,
      zs[i] * zMult]
  })
}

function powDiffFunc (data) {
  if (data.t) return zipWindowTvY(data.t, data.diff)
  return zipWindowHvY(data.diff, data.window, 1, data.offset)
}

function circulationFunc (chartData) {
  let yMax = 0
  let h = -1
  const addDough = (newHeight) => {
    while (h < newHeight) {
      h++
      yMax += blockReward(h) * atomsToDCR
    }
  }
  const heights = chartData.h
  const times = chartData.t
  const supplies = chartData.supply
  const anonymitySet = chartData.anonymitySet
  const isHeightAxis = chartData.axis === 'height'
  let xFunc, hFunc
  if (chartData.bin === 'day') {
    xFunc = isHeightAxis ? i => heights[i] : i => new Date(times[i] * 1000)
    hFunc = i => heights[i]
  } else {
    xFunc = isHeightAxis ? i => i : i => new Date(times[i] * 1000)
    hFunc = i => i
  }

  const inflation = []
  const data = map(supplies, (n, i) => {
    const height = hFunc(i)
    addDough(height)
    inflation.push(yMax)
    return [xFunc(i), supplies[i] * atomsToDCR, null, anonymitySet[i] * atomsToDCR]
  })

  const dailyBlocks = aDay / avgBlockTime
  const lastPt = data[data.length - 1]
  let x = lastPt[0]
  // Set yMax to the start at last actual supply for the prediction line.
  yMax = lastPt[1]
  if (!isHeightAxis) x = x.getTime()
  xFunc = isHeightAxis ? xx => xx : xx => { return new Date(xx) }
  const xIncrement = isHeightAxis ? dailyBlocks : aDay
  const projection = 6 * aMonth
  data.push([xFunc(x), null, yMax, null])
  for (let i = 1; i <= projection; i++) {
    addDough(h + dailyBlocks)
    x += xIncrement
    data.push([xFunc(x), null, yMax, null])
  }
  return { data, inflation }
}

function mapDygraphOptions (data, labelsVal, isDrawPoint, yLabel, labelsMG, labelsMG2) {
  return merge({
    file: data,
    labels: labelsVal,
    drawPoints: isDrawPoint,
    ylabel: yLabel,
    labelsKMB: labelsMG2 && labelsMG ? false : labelsMG,
    labelsKMG2: labelsMG2 && labelsMG ? false : labelsMG2
  }, nightModeOptions(darkEnabled()))
}

export default class extends Controller {
  static get targets () {
    return [
      'chartWrapper',
      'labels',
      'chartsView',
      'chartSelect',
      'zoomSelector',
      'zoomOption',
      'scaleType',
      'axisOption',
      'binSelector',
      'scaleSelector',
      'binSize',
      'legendEntry',
      'legendMarker',
      'modeSelector',
      'modeOption',
      'rawDataURL',
      'chartName',
      'chartTitleName',
      'chainTypeSelected',
      'vSelectorItem',
      'vSelector',
      'ticketsPurchase',
      'anonymitySet',
      'ticketsPrice',
      'rangeSelector',
      'rangeOption',
      'marketPrice',
      'chartDescription'
    ]
  }

  async connect () {
    this.isHomepage = !window.location.href.includes('/charts')
    this.query = new TurboQuery()
    ticketPoolSizeTarget = parseInt(this.data.get('tps'))
    premine = parseInt(this.data.get('premine'))
    stakeValHeight = parseInt(this.data.get('svh'))
    stakeShare = parseInt(this.data.get('pos')) / 10.0
    baseSubsidy = parseInt(this.data.get('bs'))
    subsidyInterval = parseInt(this.data.get('sri'))
    subsidyExponent = parseFloat(this.data.get('mulSubsidy')) / parseFloat(this.data.get('divSubsidy'))
    // Default to first available option in the chain-chart dropdown.
    // Falls back to 'btc' if the select isn't ready yet.
    const firstChartOpt = $('.chain-chart-vodiapicker option').first().val()
    this.chainType = firstChartOpt || 'btc'
    // init for avgBlockTime
    const _this = this
    chainList.forEach((chain, idx) => {
      const chainAvgBT = _this.data.get(chain + 'Blocktime')
      if (chainAvgBT) {
        // set for array
        targetTimePerBlock[idx] = parseInt(chainAvgBT) * 1000
      }
    })
    avgBlockTime = targetTimePerBlock[chainList.indexOf(this.chainType)]
    globalChainType = this.chainType
    legendElement = this.labelsTarget
    windowScales = commonWindowScales
    multiYAxisChart = chainMultiYAxisChart
    // Prepare the legend element generators.
    const lm = this.legendMarkerTarget
    lm.remove()
    lm.removeAttribute('data-charts-target')
    legendMarker = () => {
      const node = document.createElement('div')
      node.appendChild(lm.cloneNode())
      return node.innerHTML
    }
    legendColorMaker = (color) => {
      const clone = lm.cloneNode()
      clone.style.borderBottomColor = color
      return clone.outerHTML
    }
    const le = this.legendEntryTarget
    le.remove()
    le.removeAttribute('data-charts-target')
    legendEntry = s => {
      const node = le.cloneNode()
      node.innerHTML = s
      return node
    }

    this.settings = TurboQuery.nullTemplate(['chart', 'zoom', 'scale', 'bin', 'axis', 'visibility', 'home', 'range', 'mode'])
    if (!this.isHomepage) {
      this.query.update(this.settings)
    }
    this.defaultScale = 'linear'
    this.defaultBin = 'day'
    this.defaultRange = 'after'
    this.defaultAxis = 'time'
    this.defaultMode = 'smooth'
    this.binButtons = this.binSelectorTarget.querySelectorAll('button')
    this.scaleButtons = this.scaleSelectorTarget.querySelectorAll('button')
    this.modeButtons = this.modeSelectorTarget.querySelectorAll('button')
    this.initForChainChartChainTypeSelector()
    this.zoomCallback = this._zoomCallback.bind(this)
    this.drawCallback = this._drawCallback.bind(this)
    this.limits = null
    this.lastZoom = null
    this.visibility = []
    this.setRange(this.settings.range ? this.settings.range : 'after')
    if (this.settings.visibility) {
      this.settings.visibility.split('-', -1).forEach(s => {
        _this.visibility.push(s === 'true')
      })
    }
    Dygraph = await getDefault(
      import(/* webpackChunkName: "dygraphs" */ '../vendor/dygraphs.min.js')
    )
    this.drawInitialGraph()
    this.processNightMode = (params) => {
      this.chartsView.updateOptions(
        nightModeOptions(params.nightMode)
      )
    }
    globalEventBus.on('NIGHT_MODE', this.processNightMode)
    this._onDocClick = this.onDocClick.bind(this)
    this._onKeydown = this.onKeydown.bind(this)
  }

  initForChainChartChainTypeSelector () {
    const chainArray = []
    const chainIconArray = []
    const chainNameArr = []
    $('.chain-chart-vodiapicker option').each(function () {
      const img = $(this).attr('data-thumbnail')
      const text = this.innerText
      const value = $(this).val()
      const item = `<li><img src="${img}" alt="" value="${value}"/><span>${text}</span></li>`
      chainArray.push(item)
      chainIconArray.push(`<li class="d-flex ai-center"><img src="${img}" alt="" value="${value}"/><span>${text}</span>
        <span class="dropdown-arrow" style="margin-left: 0.5rem;">
        <svg width="8" height="5" viewBox="0 0 10 6" xmlns="http://www.w3.org/2000/svg" fill="currentColor">
          <path d="M0 0L5 6L10 0H0Z" />
        </svg>
      </span>
        </li>`)
      chainNameArr.push(value)
    })
    $('#chainChartSelectUl').html(chainArray)
    const chainIndex = chainNameArr.indexOf(this.chainType)
    if (chainIndex >= 0) {
      $('.chain-chart-selected-btn').html(chainIconArray[chainIndex])
      $('.chain-chart-selected-btn').attr('value', this.chainType)
    }
    const _this = this
    $('#chainChartSelectUl li').click(function () {
      const value = $(this).find('img').attr('value')
      if (value === _this.chainType) {
        _this.toggleSelection('.chain-chart-selection-area')
        return
      }
      _this.chainTypeSelectedTarget.value = value
      _this.chainTypeChange()
      const img = $(this).find('img').attr('src')
      const text = this.innerText
      const item = `<li class="d-flex ai-center"><img src="${img}" alt=""/><span class="ms-1">${text}</span>
      <span class="dropdown-arrow" style="margin-left: 0.5rem;">
        <svg width="8" height="5" viewBox="0 0 10 6" xmlns="http://www.w3.org/2000/svg" fill="currentColor">
          <path d="M0 0L5 6L10 0H0Z" />
        </svg>
      </span>
      </li>`
      $('.chain-chart-selected-btn').html(item)
      $('.chain-chart-selected-btn').attr('value', value)
      _this.toggleSelection('.chain-chart-selection-area')
    })
    $('.chain-chart-selected-btn').click(function () {
      _this.toggleSelection('.chain-chart-selection-area')
    })
    this.settings.chart = this.settings.chart || (this.chainType === 'dcr' ? 'ticket-price' : 'block-size')
    this.defaultChart = this.chainType === 'dcr' ? 'ticket-price' : 'block-size'
    this.chartSelectTarget.innerHTML = this.chainType === 'dcr' ? this.getDecredChartOptsHtml() : this.getMutilchainChartOptsHtml()
    this.chartSelectTarget.value = this.settings.chart
    this.handlerChainChartHeaderLink()
    this.setDisplayChainChartFooter()
  }

  handlerChainChartHeaderLink () {
    const chartHeader = document.getElementById('chainChartHeader')
    const chain = this.chainType
    const chart = this.settings.chart
    let link = chain === 'dcr' ? '/decred/charts' : `/${chain}/charts`
    link += `?chart=${chart}`
    chartHeader.href = link
  }

  chainChartHeaderClick (e) {
    if (e.target.closest('[data-homecharts-target="chartDescription"]')) {
      e.preventDefault()
    }
  }

  setDisplayChainChartFooter () {
    const chain = this.chainType
    if (chain === 'dcr' || chain === 'xmr') {
      $('#chainChartFooterRow').removeClass('d-hide')
    } else {
      $('#chainChartFooterRow').addClass('d-hide')
      this.settings.axis = 'time'
      this.settings.bin = 'day'
      this.setActiveOptionBtn(this.settings.axis, this.axisOptionTargets)
      this.setActiveToggleBtn(this.settings.bin, this.binButtons)
    }
  }

  toggleSelection (selector) {
    const target = $(selector)
    if (target.css('display') === 'none') {
      target.show()
    } else {
      target.hide()
    }
  }

  setRange (e) {
    const target = e.srcElement || e.target
    const option = target ? target.dataset.option : e
    if (!option) return
    this.setActiveOptionBtn(option, this.rangeOptionTargets)
    if (!target) return // Exit if running for the first time.
    selectedChart = null // Force fetch
    this.selectChart()
  }

  setVisibility (e) {
    switch (this.chartSelectTarget.value) {
      case 'ticket-price':
        if (!this.ticketsPriceTarget.checked && !this.ticketsPurchaseTarget.checked) {
          e.currentTarget.checked = true
          return
        }
        this.visibility = [this.ticketsPriceTarget.checked, this.ticketsPurchaseTarget.checked]
        break
      case 'coin-supply':
        this.visibility = [true, true, this.anonymitySetTarget.checked]
        break
      case 'privacy-participation':
        this.visibility = [true, this.anonymitySetTarget.checked]
        break
      case 'avg-age-days':
      case 'coin-days-destroyed':
      case 'coin-age-bands':
      case 'mean-coin-age':
      case 'total-coin-days':
        this.visibility = [true, true, this.marketPriceTarget.checked]
        break
      default:
        return
    }
    this.chartsView.updateOptions({ visibility: this.getVisibilityForCharts(this.chartSelectTarget.value, this.visibility) })
    this.settings.visibility = this.visibility.join('-')
    if (!this.isHomepage) {
      this.query.replace(this.filteredSettings())
    }
  }

  getVisibilityForCharts (chart, visibility) {
    if (!isCoinAgeChart(chart)) {
      return visibility
    }
    // if is not coin-age-bands. Return [true, visible]
    if (chart !== 'coin-age-bands') {
      return [true, visibility[2]]
    }
    // if is coin-age-bands, push for each band item
    const stackVisibility = []
    coinAgeBandsColors.forEach((item) => {
      stackVisibility.push(true)
    })
    stackVisibility[stackVisibility.length - 1] = visibility[2]
    return stackVisibility
  }

  disconnect () {
    globalEventBus.off('NIGHT_MODE', this.processNightMode)
    if (this.chartsView !== undefined) {
      this.chartsView.destroy()
    }
    selectedChart = null
    document.removeEventListener('click', this._onDocClick)
    document.removeEventListener('keydown', this._onKeydown)
  }

  toggleChartDescription (e) {
    const SELECTOR = '[data-ctooltip]'
    const OPEN = 'is-open'
    const trigger = e.currentTarget
    const target = trigger.matches(SELECTOR) ? trigger : trigger.closest(SELECTOR) || trigger

    // create bubble first time
    let bubble = target.querySelector('.ctooltip-bubble')
    const ctooltip = target.getAttribute('data-ctooltip') ||
        target.getAttribute('data-tooltip') ||
        ''
    if (!bubble) {
      bubble = document.createElement('div')
      bubble.className = 'ctooltip-bubble'
      bubble.textContent = ctooltip
      target.appendChild(bubble)
    } else {
      bubble.textContent = ctooltip
    }

    // close difference & toggle current
    this.element.querySelectorAll(`${SELECTOR}.${OPEN}`).forEach(el => { if (el !== target) el.classList.remove(OPEN) })
    target.classList.toggle(OPEN)

    const anyOpen = this.element.querySelector(`${SELECTOR}.${OPEN}`)
    if (anyOpen) {
      this._onDocClick ||= this.onDocClick.bind(this)
      this._onKeydown ||= this.onKeydown.bind(this)
      document.addEventListener('click', this._onDocClick)
      document.addEventListener('keydown', this._onKeydown)
    } else {
      document.removeEventListener('click', this._onDocClick)
      document.removeEventListener('keydown', this._onKeydown)
    }
  }

  onDocClick (e) {
    if (!this.element.contains(e.target)) return this.closeAll()
    // if click out any item [data-ctooltip] in controller scope -> close all
    if (!e.target.closest('[data-ctooltip]')) this.closeAll()
  }

  closeAll () {
    this.element
      .querySelectorAll(`${'[data-ctooltip]'}.${'is-open'}`)
      .forEach(el => el.classList.remove('is-open'))
    document.removeEventListener('click', this._onDocClick)
    document.removeEventListener('keydown', this._onKeydown)
  }

  onKeydown (e) { if (e.key === 'Escape') this.closeAll() }

  drawInitialGraph () {
    const legendWrapper = document.querySelector('.legend-wrapper')
    const _this = this
    const options = {
      axes: { y: { axisLabelWidth: 50 }, y2: { axisLabelWidth: 55 } },
      labels: ['Date', 'Ticket Price', 'Tickets Bought'],
      digitsAfterDecimal: 8,
      showRangeSelector: true,
      rangeSelectorPlotFillColor: '#C4CBD2',
      rangeSelectorAlpha: 0.4,
      rangeSelectorHeight: 40,
      drawPoints: true,
      pointSize: 0.25,
      legend: 'always',
      labelsSeparateLines: true,
      labelsDiv: legendElement,
      legendFormatter: legendFormatter,
      highlightCircleSize: 4,
      ylabel: 'Ticket Price',
      y2label: 'Tickets Bought',
      labelsUTC: true,
      axisLineColor: '#C4CBD2',
      highlightCallback: function (e, x, points) {
        legendWrapper.style.display = 'block'

        legendWrapper.innerHTML = legendFormatter({
          x: x,
          xHTML: x,
          dygraph: _this.chartsView,
          points: points,
          series: points.map(p => {
            const g = _this.chartsView
            const props = g.getPropertiesForSeries(p.name) // get đúng axis & column
            const color = g.getOption('color', p.name) || props.color

            return {
              label: p.name,
              y: p.yval,
              yHTML: formatYLegend(g, p.name, p.yval, p.idx, props.column),
              color: color,
              dashHTML: `<div class="dygraph-legend-line"
           style="border-bottom-color:${color};
                  border-bottom-style:solid;
                  border-bottom-width:4px;
                  display:inline-block;
                  margin-right:4px;"></div>`,
              labelHTML: p.name
            }
          })
        })

        const rect = _this.chartsView.graphDiv.getBoundingClientRect()
        const relX = e.clientX - rect.left
        const relY = e.clientY - rect.top

        let left = relX - legendWrapper.offsetWidth - 10
        let top = relY - 5

        // Flip horizontally if left edge touched (so với viewport)
        if (left + rect.left < 0) {
          left = relX + 10
        }
        // Flip horizontally if right edge touched (so với viewport)
        if (left + rect.left + legendWrapper.offsetWidth > window.innerWidth) {
          left = relX - legendWrapper.offsetWidth - 10
        }

        // Flip vertically if touching the ceiling
        if (top + rect.top < 0) {
          top = relY + 10
        }
        // Flip vertically if bottomed out
        if (top + rect.top + legendWrapper.offsetHeight > window.innerHeight) {
          top = relY - legendWrapper.offsetHeight - 10
        }

        legendWrapper.style.left = left + 'px'
        legendWrapper.style.top = top + 'px'
      },
      unhighlightCallback: function () {
        legendWrapper.style.display = 'none'
      }
    }

    this.chartsView = new Dygraph(
      this.chartsViewTarget,
      [[1, 1, 5], [2, 5, 11]],
      options
    )
    const graphDiv = _this.chartsView.graphDiv

    graphDiv.addEventListener('mouseleave', () => {
      legendWrapper.style.display = 'none'
    })

    this.chartSelectTarget.value = this.settings.chart

    if (this.settings.axis) this.setAxis(this.settings.axis) // set first
    if (this.settings.scale === 'log') this.setSelectScale(this.settings.scale)
    if (this.settings.zoom) this.setInitSelectZoom(this.settings.zoom)
    this.setSelectBin(this.settings.bin ? this.settings.bin : 'day')
    this.setSelectMode(this.settings.mode ? this.settings.mode : 'smooth')

    const ogLegendGenerator = Dygraph.Plugins.Legend.generateLegendHTML
    Dygraph.Plugins.Legend.generateLegendHTML = (g, x, pts, w, row) => {
      g.updateOptions({ legendIndex: row }, true)
      return ogLegendGenerator(g, x, pts, w, row)
    }
    this.selectChart()
  }

  plotGraph (chartName, data) {
    let d = []
    const logScale = isScaleDisabled(chartName) ? false : this.settings.scale === 'log'
    let gOptions = {
      zoomCallback: null,
      drawCallback: null,
      logscale: logScale,
      valueRange: [null, null],
      visibility: null,
      y2label: null,
      y3label: null,
      stepPlot: this.settings.mode === 'stepped',
      axes: {},
      series: null,
      inflation: null,
      plotter: null,
      fillGraph: false,
      stackedGraph: false
    }
    rawPoolValue = []
    rawCoinSupply = []
    const labels = []
    const stackVisibility = []
    yFormatter = defaultYFormatter
    const xlabel = data.t ? 'Date' : 'Block Height'
    const _this = this
    const isHeightBlock = data.axis === 'height' && data.bin === 'block'
    switch (chartName) {
      case 'ticket-price': // price graph
        d = ticketPriceFunc(data)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Price', 'Tickets Bought'], true,
          'Price (DCR)', true, false))
        gOptions.y2label = 'Tickets Bought'
        gOptions.series = { 'Tickets Bought': { axis: 'y2' } }
        this.visibility = [this.ticketsPriceTarget.checked, this.ticketsPurchaseTarget.checked]
        gOptions.visibility = this.visibility
        gOptions.axes.y2 = {
          valueRange: [0, windowSize * 20 * 8],
          axisLabelFormatter: (y) => Math.round(y),
          axisLabelWidth: isMobile() ? yAxisLabelWidth.y2['ticket-price'] : yAxisLabelWidth.y2['ticket-price']
        }
        yFormatter = customYFormatter(y => {
          if (y == null || isNaN(y)) return '–'
          return y.toFixed(8) + ' DCR'
        })
        break
      case 'ticket-pool-size': // pool size graph
        d = poolSizeFunc(data)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Ticket Pool Size', 'Network Target'],
          false, 'Ticket Pool Size', true, false))
        gOptions.series = {
          'Network Target': {
            strokePattern: [5, 3],
            connectSeparatedPoints: true,
            strokeWidth: 2,
            color: '#C4CBD2'
          }
        }
        yFormatter = customYFormatter(y => `${intComma(y)} tickets<br>(network target ${intComma(ticketPoolSizeTarget)})`)
        break
      case 'stake-participation':
        d = percentStakedFunc(data)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Stake Participation'], true,
          'Stake Participation (%)', true, false))
        yFormatter = (div, data, i) => {
          if (!data.series || data.series.length === 0) return
          addLegendEntryFmt(div, data.series[0], y => y.toFixed(4) + '%')
          div.appendChild(legendEntry(`${legendMarker()} Ticket Pool Value: ${intComma(rawPoolValue[i])} DCR`))
          div.appendChild(legendEntry(`${legendMarker()} Coin Supply: ${intComma(rawCoinSupply[i])} DCR`))
        }
        break
      case 'privacy-participation': { // anonymity set graph
        d = anonymitySetFunc(data)
        this.customLimits = d.limits
        const label = 'Mix Rate'
        assign(gOptions, mapDygraphOptions(d.data, [xlabel, label], false, `${label} (DCR)`, true, false))

        yFormatter = (div, data, i) => {
          if (!data.series || data.series.length === 0) return
          addLegendEntryFmt(div, data.series[0], y => (y > 0 ? intComma(y) : '0') + ' DCR')
        }
        break
      }
      case 'ticket-pool-value': // pool value graph
        d = zip2D(data, data.poolval, atomsToDCR)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Ticket Pool Value'], true,
          'Ticket Pool Value (DCR)', true, false))
        yFormatter = customYFormatter(y => {
          if (y == null || isNaN(y)) return '–'
          return intComma(y) + ' DCR'
        })
        break
      case 'block-size': // block size graph
        d = zip2D(data, data.size)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Block Size'], false, 'Block Size', false, true))
        break
      case 'blockchain-size': // blockchain size graph
        d = zip2D(data, data.size)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Blockchain Size'], true,
          'Blockchain Size', false, true))
        break
      case 'tx-count': // tx per block graph
        d = zip2D(data, data.count)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Total Transactions'], false,
          'Total Transactions', true, false))
        break
      case 'tx-per-block': // tx per block graph
        d = zip2D(data, data.count)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Avg TXs Per Block'], false,
          'Avg TXs Per Block', true, false))
        break
      case 'mined-blocks': // tx per block graph
        d = zip2D(data, data.count)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Mined Blocks'], false,
          'Mined Blocks', true, false))
        break
      case 'mempool-txs': // tx per block graph
        d = zip2D(data, data.count)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Mempool Transactions'], false,
          'Mempool Transactions', true, false))
        break
      case 'mempool-size': // blockchain size graph
        d = zip2D(data, data.size)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Mempool Size'], true,
          'Mempool Size', false, true))
        break
      case 'address-number': // tx per block graph
        d = zip2D(data, data.count)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Active Addresses'], false,
          'Active Addresses', true, false))
        break
      case 'pow-difficulty': // difficulty graph
        d = _this.chainType === 'dcr' ? powDiffFunc(data) : zip2D(data, data.diff)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Difficulty'], true, 'Difficulty', true, false))
        if (_this.chainType === 'dcr' && _this.settings.range !== 'after' && _this.settings.scale !== 'log') {
          gOptions.plotter = _this.settings.axis === 'height' ? difficultyBlockPlotter : difficultyTimePlotter
        }
        break
      case 'missed-votes':
        d = missedVotesFunc(data)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Missed Votes'], false,
          'Missed Votes per Window', true, false))
        break
      case 'coin-supply': // supply graph
        if (this.chainType === 'dcr') {
          d = circulationFunc(data)
          assign(gOptions, mapDygraphOptions(d.data, [xlabel, 'Coin Supply', 'Inflation Limit', 'Mix Rate'],
            true, 'Coin Supply (' + this.chainType.toUpperCase() + ')', true, false))
          gOptions.y2label = 'Inflation Limit'
          gOptions.y3label = 'Mix Rate'
          gOptions.series = { 'Inflation Limit': { axis: 'y2' }, 'Mix Rate': { axis: 'y3' } }
          this.visibility = [true, true, this.anonymitySetTarget.checked]
          gOptions.visibility = this.visibility
          gOptions.series = {
            'Inflation Limit': {
              strokePattern: [5, 5],
              color: '#C4CBD2',
              strokeWidth: 1.5
            },
            'Mix Rate': {
              color: '#2dd8a3'
            }
          }
          gOptions.inflation = d.inflation
          yFormatter = (div, data, i) => {
            if (!data.series || data.series.length === 0) return
            addLegendEntryFmt(div, data.series[0], y => intComma(y) + ' ' + globalChainType.toUpperCase())
            let change = 0
            if (i < d.inflation.length) {
              const supply = data.series[0].y
              if (this.anonymitySetTarget.checked) {
                const mixed = data.series[2].y
                const mixedPercentage = ((mixed / supply) * 100).toFixed(2)
                div.appendChild(legendEntry(`${legendColorMaker('#17b080ff')} Mixed: ${intComma(mixed)} DCR (${mixedPercentage}%)`))
              }
              const predicted = d.inflation[i]
              const unminted = predicted - data.series[0].y
              change = ((unminted / predicted) * 100).toFixed(2)
              div.appendChild(legendEntry(`${legendMarker()} Unminted: ${intComma(unminted)} ` + globalChainType.toUpperCase() + ` (${change}%)`))
            }
          }
        } else {
          d = zip2D(data, data.supply)
          assign(gOptions, mapDygraphOptions(d, [xlabel, 'Coins Supply (' + globalChainType.toUpperCase() + ')'], true,
            'Coins Supply (' + globalChainType.toUpperCase() + ')', true, false))
          yFormatter = (div, data, i) => {
            addLegendEntryFmt(div, data.series[0], y => intComma(y) + ' ' + globalChainType.toUpperCase())
          }
        }
        break

      case 'fees': // block fee graph
        d = zip2D(data, data.fees, unitToCoin(this.chainType))
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Total Fee'], false, 'Total Fee (' + globalChainType.toUpperCase() + ')', true, false))
        yFormatter = customYFormatter(y => {
          if (y == null || isNaN(y)) return '–'
          return y.toFixed(8) + ' ' + globalChainType.toUpperCase()
        })
        break

      case 'duration-btw-blocks': // Duration between blocks graph
        d = zip2D(data, data.duration, 1, 1)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Duration Between Blocks'], false,
          'Duration Between Blocks (seconds)', true, false))
        break

      case 'hashrate': // Total chainwork over time
        d = this.chainType === 'dcr' ? (isHeightBlock ? zipHvY(data.h, data.rate, 1, data.offset) : zip2D(data, data.rate, 1, data.offset)) : zip2D(data, data.rate, 1, data.offset)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Network Hashrate'],
          false, 'Network Hashrate', false, true))
        yFormatter = customYFormatter(y => withBigUnits(y, hashrateUnits))
        if (_this.chainType === 'dcr' && _this.settings.range !== 'after' && _this.settings.scale !== 'log') {
          gOptions.plotter = _this.settings.axis === 'height' ? hashrateBlockPlotter : hashrateTimePlotter
        }
        break

      case 'avg-age-days':
        d = avgAgeDaysFunc(data)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Average Age Days', 'Decred Price'], true,
          'Average Age Days (days)', true, false))
        gOptions.y2label = 'Decred Price (USD)'
        gOptions.series = { 'Decred Price': { axis: 'y2' } }
        this.visibility = [true, true, this.marketPriceTarget.checked]
        gOptions.visibility = [true, this.marketPriceTarget.checked]
        gOptions.axes.y2 = {
          valueRange: [0, 300],
          axisLabelFormatter: (y) => Math.round(y),
          axisLabelWidth: isMobile() ? yAxisLabelWidth.y2['avg-age-days'] : yAxisLabelWidth.y2['avg-age-days'] + 15
        }
        yFormatter = (div, data, i) => {
          if (!data.series || data.series.length === 0) return
          if (this.marketPriceTarget.checked) {
            addLegendEntryFmt(div, data.series[1], y => (y > 0 ? humanize.formatNumber(y, 2, true) : '0') + ' USD')
          }
          addLegendEntryFmt(div, data.series[0], y => y.toFixed(2) + ' days')
        }
        break

      case 'coin-days-destroyed':
        d = avgCoinDayDestroyedFunc(data)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Coin Days Destroyed', 'Decred Price'], true,
          'Coin Days Destroyed (coin-days)', true, false))
        gOptions.y2label = 'Decred Price (USD)'
        gOptions.series = { 'Decred Price': { axis: 'y2' } }
        this.visibility = [true, true, this.marketPriceTarget.checked]
        gOptions.visibility = [true, this.marketPriceTarget.checked]
        gOptions.axes.y2 = {
          valueRange: [0, 300],
          axisLabelFormatter: (y) => Math.round(y),
          axisLabelWidth: isMobile() ? yAxisLabelWidth.y2['coin-days-destroyed'] : yAxisLabelWidth.y2['coin-days-destroyed'] + 15
        }
        yFormatter = (div, data, i) => {
          if (!data.series || data.series.length === 0) return
          if (this.marketPriceTarget.checked) {
            addLegendEntryFmt(div, data.series[1], y => (y > 0 ? humanize.formatNumber(y, 2, true) : '0') + ' USD')
          }
          addLegendEntryFmt(div, data.series[0], y => humanize.formatNumber(y, 2, true) + ' coin-days')
        }
        break

      case 'mean-coin-age':
        d = meanCoinAgeFunc(data)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Mean Coin Age', 'Decred Price'], true,
          'Mean Coin Age (days)', true, false))
        gOptions.y2label = 'Decred Price (USD)'
        gOptions.series = { 'Decred Price': { axis: 'y2' } }
        this.visibility = [true, true, this.marketPriceTarget.checked]
        gOptions.visibility = [true, this.marketPriceTarget.checked]
        gOptions.axes.y2 = {
          valueRange: [0, 300],
          axisLabelFormatter: (y) => Math.round(y),
          axisLabelWidth: isMobile() ? yAxisLabelWidth.y2['mean-coin-age'] : yAxisLabelWidth.y2['mean-coin-age'] + 15
        }
        yFormatter = (div, data, i) => {
          if (!data.series || data.series.length === 0) return
          if (this.marketPriceTarget.checked) {
            addLegendEntryFmt(div, data.series[1], y => (y > 0 ? humanize.formatNumber(y, 2, true) : '0') + ' USD')
          }
          addLegendEntryFmt(div, data.series[0], y => humanize.formatNumber(y, 2, true) + ' days')
        }
        break

      case 'total-coin-days':
        d = totalCoinDaysFunc(data)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Total Coin Days', 'Decred Price'], true,
          'Total Coin Days (days)', true, false))
        gOptions.y2label = 'Decred Price (USD)'
        gOptions.series = { 'Decred Price': { axis: 'y2' } }
        this.visibility = [true, true, this.marketPriceTarget.checked]
        gOptions.visibility = [true, this.marketPriceTarget.checked]
        gOptions.axes.y2 = {
          valueRange: [0, 300],
          axisLabelFormatter: (y) => Math.round(y),
          axisLabelWidth: isMobile() ? yAxisLabelWidth.y2['total-coin-days'] : yAxisLabelWidth.y2['total-coin-days'] + 15
        }
        yFormatter = (div, data, i) => {
          if (!data.series || data.series.length === 0) return
          if (this.marketPriceTarget.checked) {
            addLegendEntryFmt(div, data.series[1], y => (y > 0 ? humanize.formatNumber(y, 2, true) : '0') + ' USD')
          }
          addLegendEntryFmt(div, data.series[0], y => humanize.formatNumber(y, 2, true) + ' coin-days')
        }
        break

      case 'coin-age-bands':
        d = coindAgeBandsFunc(data)
        labels.push(xlabel)
        labels.push(...coinAgeBandsLabels)
        this.visibility = [true, true, this.marketPriceTarget.checked]
        coinAgeBandsColors.forEach((item) => {
          stackVisibility.push(true)
        })
        stackVisibility[stackVisibility.length - 1] = this.visibility[2]
        gOptions = {
          labels: labels,
          file: d,
          logscale: false,
          colors: coinAgeBandsColors,
          ylabel: 'HODL Wave (%)',
          y2label: 'Decred Price (USD)',
          valueRange: [0, 100],
          fillGraph: true,
          stackedGraph: true,
          visibility: stackVisibility,
          series: {
            none: { fillGraph: true },
            '>7Y': { fillGraph: true },
            '5-7Y': { fillGraph: true },
            '3-5Y': { fillGraph: true },
            '2-3Y': { fillGraph: true },
            '1-2Y': { fillGraph: true },
            '6M-1Y': { fillGraph: true },
            '1-6M': { fillGraph: true },
            '1W-1M': { fillGraph: true },
            '1D-1W': { fillGraph: true },
            '<1D': { fillGraph: true },
            'Decred Price': {
              axis: 'y2',
              strokeWidth: 1,
              fillGraph: false
            }
          },
          legend: 'always',
          includeZero: true,
          // zoomCallback: this.depthZoomCallback,
          axes: {
            y2: {
              valueRange: [0, 300],
              axisLabelFormatter: (y) => Math.round(y),
              axisLabelWidth: isMobile() ? yAxisLabelWidth.y2['coin-age-bands'] : yAxisLabelWidth.y2['coin-age-bands'] + 15
            }
          }
        }
        yFormatter = (div, data, i) => {
          if (!data.series || data.series.length === 0) return
          if (this.marketPriceTarget.checked) {
            addLegendEntryFmt(div, data.series[data.series.length - 1], y => (y > 0 ? humanize.formatNumber(y, 2, true) : '0') + ' USD')
          }
          data.series.forEach((serie, idx) => {
            if (idx === 0 || idx === data.series.length - 1) {
              return
            }
            addLegendEntryFmt(div, serie, y => (y > 0 ? humanize.formatNumber(y, 2, true) : '0') + ' %')
          })
        }
        break
      case 'total-ring-size': // total ring-size for monero
        d = zip2D(data, data.ringSize)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Total Ring Size'], false,
          'Total Ring Size', true, false))
        break
      case 'avg-ring-size': // total ring-size for monero
        d = zip2D(data, data.ringSize)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Avg Ring Size / Input'], false,
          'Avg Ring Size / Input', true, false))
        break
      case 'fee-rate':
        d = zip2D(data, data.fees, unitToCoin(this.chainType))
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Fee Rate (XMR/kB)'], false,
          'Fee Rate (XMR/kB)', true, false))
        break
      case 'avg-tx-size':
        d = zip2D(data, data.size)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Average Tx Size'], true,
          'Average Tx Size', false, true))
        break
      case 'decoy-bands':
        d = decoyBandsFunc(data)
        labels.push(xlabel)
        labels.push(...decoyBandsLabels)
        decoyBandsColors.forEach((item) => {
          stackVisibility.push(true)
        })
        gOptions = {
          labels: labels,
          file: d,
          logscale: false,
          colors: decoyBandsColors,
          ylabel: 'Decoys (%)',
          y2label: 'Mixin',
          valueRange: [0, 100],
          fillGraph: true,
          stackedGraph: true,
          visibility: stackVisibility,
          series: {
            none: { fillGraph: true },
            'No Tx': { fillGraph: true },
            'Decoys 0-3': { fillGraph: true },
            'Decoys 4-7': { fillGraph: true },
            'Decoys 8-11': { fillGraph: true },
            'Decoys 12-14': { fillGraph: true },
            'Decoys >15': { fillGraph: true },
            Mixin: {
              axis: 'y2',
              strokeWidth: 1,
              fillGraph: false
            }
          },
          legend: 'always',
          includeZero: true,
          // zoomCallback: this.depthZoomCallback,
          axes: {
            y2: {
              valueRange: [0, 3500000],
              axisLabelFormatter: (y) => Math.round(y),
              axisLabelWidth: isMobile() ? yAxisLabelWidth.y2['decoy-bands'] : yAxisLabelWidth.y2['decoy-bands'] + 15
            }
          }
        }
        yFormatter = (div, data, i) => {
          if (!data.series || data.series.length === 0) return
          addLegendEntryFmt(div, data.series[data.series.length - 1], y => y)
          data.series.forEach((serie, idx) => {
            if (idx === 0 || idx === data.series.length - 1) {
              return
            }
            addLegendEntryFmt(div, serie, y => (y > 0 ? humanize.formatNumber(y, 2, true) : '0') + ' %')
          })
        }
        break
    }
    gOptions.axes.y = {
      axisLabelWidth: isMobile() ? yAxisLabelWidth.y1[chartName] : yAxisLabelWidth.y1[chartName] + 10
    }
    const baseURL = `${this.query.url.protocol}//${this.query.url.host}`
    this.rawDataURLTarget.textContent = this.chainType === 'dcr'
      ? `${baseURL}/api/chart/${chartName}?axis=${this.settings.axis}&bin=${this.settings.bin}`
      : `${baseURL}/api/chainchart/${this.chainType}/${chartName}?axis=${this.settings.axis}&bin=${this.settings.bin}`

    this.chartsView.plotter_.clear()
    this.chartsView.updateOptions(gOptions, false)
    if (yValueRanges[chartName]) this.supportedYRange = this.chartsView.yAxisRanges()
    this.validateZoom()
  }

  updateVSelector (chart) {
    if (!chart) {
      chart = this.chartSelectTarget.value
    }
    const that = this
    let showWrapper = false
    this.vSelectorItemTargets.forEach(el => {
      let show = el.dataset.charts.indexOf(chart) > -1
      if (el.dataset.bin && el.dataset.bin.indexOf(that.selectedBin()) === -1) {
        show = false
      }
      if (el.dataset.charts === 'coin-age' && isCoinAgeChart(chart)) {
        show = true
      }
      if (show) {
        el.classList.remove('d-hide')
        showWrapper = true
      } else {
        el.classList.add('d-hide')
      }
    })
    if (showWrapper) {
      this.vSelectorTarget.classList.remove('d-hide')
    } else {
      this.vSelectorTarget.classList.add('d-hide')
    }
    this.setVisibilityFromSettings()
  }

  async chainTypeChange () {
    // selected chain
    const chain = this.chainTypeSelectedTarget.value
    if (chain === this.chainType) {
      return
    }
    const _this = this
    let reinitChartType = false
    reinitChartType = this.chainType === 'dcr' || chain === 'dcr' || this.chainType === 'xmr' || chain === 'xmr'
    this.chainType = chain
    globalChainType = chain
    avgBlockTime = targetTimePerBlock[chainList.indexOf(this.chainType)]
    windowScales = chain === 'dcr' ? decredWindowScales : commonWindowScales
    multiYAxisChart = chain === 'dcr' ? decredMultiYAxisChart : chainMultiYAxisChart
    // reinit
    if (reinitChartType) {
      this.chartSelectTarget.innerHTML = this.chainType === 'dcr' ? this.getDecredChartOptsHtml() : this.getMutilchainChartOptsHtml()
    }
    // determind select chart
    this.settings.chart = this.getSelectedChart()
    this.handlerChainChartHeaderLink()
    this.setDisplayChainChartFooter()
    this.chartNameTarget.textContent = this.getChartName(this.chartSelectTarget.value)
    this.chartTitleNameTarget.textContent = this.chartNameTarget.textContent
    this.chartDescriptionTarget.dataset.ctooltip = this.getChartDescriptionTooltip(this.settings.chart)
    this.customLimits = null
    this.chartWrapperTarget.classList.add('loading')
    if (isScaleDisabled(this.settings.chart)) {
      this.scaleSelectorTarget.classList.add('d-hide')
    } else {
      this.scaleSelectorTarget.classList.remove('d-hide')
    }
    if (isModeEnabled(this.settings.chart)) {
      this.modeSelectorTarget.classList.remove('d-hide')
    } else {
      this.modeSelectorTarget.classList.add('d-hide')
    }
    if (isBinDisabled(this.settings.chart)) {
      this.settings.bin = 'day'
      this.setActiveToggleBtn(this.settings.bin, this.binButtons)
      this.binSelectorTarget.classList.add('d-hide')
    } else if (this.chainType === 'dcr' || this.chainType === 'xmr') {
      this.binSelectorTarget.classList.remove('d-hide')
    }
    if (hasMultipleVisibility(this.settings.chart)) {
      this.vSelectorTarget.classList.remove('d-hide')
      this.updateVSelector(this.settings.chart)
    } else {
      this.vSelectorTarget.classList.add('d-hide')
    }
    if (useRange(this.settings.chart) && this.chainType === 'dcr') {
      this.settings.range = this.selectedRange()
      this.rangeSelectorTarget.classList.remove('d-hide')
    } else {
      this.settings.range = ''
      this.rangeSelectorTarget.classList.add('d-hide')
    }
    if (selectedType !== this.chainType || (selectedType === this.chainType && selectedChart !== this.settings.chart) ||
      this.settings.bin !== this.selectedBin() || this.settings.axis !== this.selectedAxis()) {
      let url = this.chainType === 'dcr' ? '/api/chart/' + this.settings.chart : `/api/chainchart/${this.chainType}/` + this.settings.chart
      if (usesWindowUnits(this.settings.chart) && !usesHybridUnits(this.settings.chart)) {
        this.binSelectorTarget.classList.add('d-hide')
        this.settings.bin = 'window'
      } else if (!isBinDisabled(this.settings.chart)) {
        this.settings.bin = this.selectedBin()
        if (this.chainType === 'dcr' || this.chainType === 'xmr') {
          this.binSelectorTarget.classList.remove('d-hide')
          // handler for option window only
          this.binButtons.forEach(btn => {
            if (btn.name !== 'window') return
            if (usesHybridUnits(_this.settings.chart)) {
              btn.classList.remove('d-hide')
            } else {
              btn.classList.add('d-hide')
              if (_this.settings.bin === 'window') {
                _this.settings.bin = 'day'
                _this.setActiveToggleBtn(_this.settings.bin, _this.binButtons)
              }
            }
          })
          // if bin is blocks (xmr), hide option 'All' in zoom
          if (this.chainType === 'xmr' && this.settings.bin === 'block') {
            // if zoom is all, change to year
            const selectedZoom = this.zoomSelectorTarget.value
            // hide 'All' option
            const optionHtml = `<option value="year">Year</option>
                                <option value="month">Month</option>
                                <option value="week">Week</option>
                                <option value="day">Day</option>`
            this.zoomSelectorTarget.innerHTML = optionHtml
            if (!selectedZoom || selectedZoom === '' || selectedZoom === 'all') {
              this.settings.zoom = ''
              this.zoomSelectorTarget.value = 'year'
            } else {
              this.zoomSelectorTarget.value = selectedZoom
            }
          } else {
            const selectedZoom = this.zoomSelectorTarget.value
            // else, show 'All' option in zoom
            const optionHtml = `<option value="all">All</option>
                                <option value="year">Year</option>
                                <option value="month">Month</option>
                                <option value="week">Week</option>
                                <option value="day">Day</option>`
            this.zoomSelectorTarget.innerHTML = optionHtml
            this.zoomSelectorTarget.value = selectedZoom
          }
        } else {
          this.settings.bin = 'day'
          this.setActiveToggleBtn(this.settings.bin, this.binButtons)
          this.binSelectorTarget.classList.add('d-hide')
          const selectedZoom = this.zoomSelectorTarget.value
          // else, show 'All' option in zoom
          const optionHtml = `<option value="all">All</option>
                                <option value="year">Year</option>
                                <option value="month">Month</option>
                                <option value="week">Week</option>
                                <option value="day">Day</option>`
          this.zoomSelectorTarget.innerHTML = optionHtml
          this.zoomSelectorTarget.value = selectedZoom
        }
      }
      url += `?bin=${this.settings.bin}`
      if (this.settings.range !== '') {
        url += `&range=${this.settings.range}`
      }
      this.settings.axis = this.selectedAxis()
      if (!this.settings.axis) this.settings.axis = 'time' // Set the default.
      url += `&axis=${this.settings.axis}`
      this.setActiveOptionBtn(this.settings.axis, this.axisOptionTargets)
      const chartResponse = await requestJSON(url)
      selectedType = this.chainType
      selectedChart = this.settings.chart
      this.plotGraph(this.settings.chart, chartResponse)
    } else {
      this.chartWrapperTarget.classList.remove('loading')
    }
  }

  getChainName () {
    switch (this.chainType) {
      case 'btc':
        return 'Bitcoin'
      case 'ltc':
        return 'Litecoin'
      case 'dcr':
        return 'Decred'
      case 'xmr':
        return 'Monero'
      default:
        return 'Unknown'
    }
  }

  getChartDescriptionTooltip (chartType) {
    switch (chartType) {
      case 'ticket-price':
        return 'Shows the historical ticket price of Decred, reflecting the cost of purchasing staking tickets over time.'
      case 'ticket-pool-size':
        return 'Displays the historical size of the Decred ticket pool, indicating the total number of tickets locked for staking over time.'
      case 'ticket-pool-value':
        return 'Represents the total value of the Decred ticket pool, showing the amount of DCR locked in staking tickets over time.'
      case 'stake-participation':
        return 'Shows the percentage of circulating DCR actively participating in staking over time.'
      case 'privacy-participation':
        return 'Indicates the percentage of circulating DCR that has gone through privacy mixing over time.'
      case 'missed-votes':
        return 'Shows the number of staking votes that were missed, meaning tickets that failed to vote when selected.'
      case 'block-size':
        return `Displays the historical average size of ${this.getChainName()} blocks, reflecting how much transaction data each block contains.`
      case 'blockchain-size':
        return `Shows the total size of the ${this.getChainName()} blockchain, representing cumulative data stored over time.`
      case 'tx-count':
        return `Displays the number of transactions recorded on the ${this.getChainName()} network over time.`
      case 'duration-btw-blocks':
        return `Shows the average time interval between consecutive ${this.getChainName()} blocks over time.`
      case 'pow-difficulty':
        return `Represents the mining difficulty of ${this.getChainName()}’s Proof-of-Work, indicating how hard it is to find a new block over time.`
      case 'chainwork':
        return 'Shows the cumulative amount of computational work contributed to secure the Decred blockchain over time.'
      case 'hashrate':
        return `Total computational power securing the ${this.getChainName()} network over time.`
      case 'coin-supply':
        return `Shows the total circulating supply of ${this.getChainName()} coins over time.`
      case 'fees':
        return `Displays the total transaction fees paid by users on the ${this.getChainName()} network over time.`
      case 'avg-age-days':
        return 'Shows the average age (in days) of coins spent in each Decred block, reflecting how long coins were held before being transacted.'
      case 'coin-days-destroyed':
        return 'Represents the total coin-days destroyed per block, measuring the age of coins moved multiplied by their amount.'
      case 'coin-age-bands':
        return 'Visualizes the distribution of Decred’s coin supply by age bands, showing how long coins have been held before moving.'
      case 'mean-coin-age':
        return 'Shows the average age of all Decred coins in circulation, indicating how long coins have been held without moving.'
      case 'total-coin-days':
        return 'Represents the cumulative total of all coin-days in the Decred network, measuring how long coins have remained unmoved.'
      case 'tx-per-block':
        return `Average number of transactions per ${this.getChainName()} block over time.`
      case 'mined-blocks':
        return `Number of ${this.getChainName()} blocks successfully mined over time.`
      case 'mempool-txs':
        return `Number of unconfirmed ${this.getChainName()} transactions waiting in the mempool over time.`
      case 'mempool-size':
        return `Total size of unconfirmed ${this.getChainName()} transactions in the mempool over time.`
      case 'address-number':
        return `Total number of unique ${this.getChainName()} addresses over time.`
      case 'total-ring-size':
        return 'Sum of all ring members (real + decoys) used across inputs in a block or time window — a measure of on-chain anonymity volume.'
      case 'avg-ring-size':
        return 'Average number of ring members per input over time — indicates the typical anonymity level per transaction input (higher = greater anonymity).'
      case 'fee-rate':
        return 'Fee-rate (XMR/kB): total fees paid per kilobyte of transactions — a measure of network fee pressure.'
      case 'avg-tx-size':
        return 'Average Transaction Size — mean transaction size'
      case 'decoy-bands':
        return 'Monero Decoys Bands — distribution of transactions by ring size (decoy / mixin bands), shown as percent of total (100% stacked).'
      default:
        return ''
    }
  }

  getSelectedChart () {
    let hasChart = false
    const chartOpts = this.chainType === 'dcr' ? decredChartOpts : (this.chainType === 'xmr' ? xmrChartOpts : mutilchainChartOpts)
    const _this = this
    chartOpts.forEach((opt) => {
      if (_this.settings.chart === opt) {
        hasChart = true
      }
    })
    if (hasChart) {
      this.chartSelectTarget.value = this.settings.chart
      return this.settings.chart
    }
    return chartOpts[0]
  }

  getDecredChartOptsHtml () {
    return `<optgroup label="Staking">
        <option value="ticket-price">Ticket Price</option>
        <option value="ticket-pool-size">Ticket Pool Size</option>
        <option value="ticket-pool-value">Ticket Pool Value</option>
        <option value="stake-participation">Stake Participation</option>
        <option value="privacy-participation">Privacy Participation</option>
        <option value="missed-votes">Missed Votes</option>
      </optgroup>
        <optgroup label="Chain">
        <option value="block-size">Block Size</option>
        <option value="blockchain-size">Blockchain Size</option>
        <option value="tx-count">Transaction Count</option>
        <option value="duration-btw-blocks">Duration Between Blocks</option>
      </optgroup>
      <optgroup label="Mining">
        <option value="pow-difficulty">PoW Difficulty</option>
        <option value="chainwork">Total Work</option>
        <option value="hashrate">Hashrate</option>
      </optgroup>
        <optgroup label="Distribution">
        <option value="coin-supply">Coin Supply</option>
        <option value="fees">Fees</option>
      </optgroup>
      </optgroup>
      <optgroup label="Coin Age">
        <option value="avg-age-days">Average Age Days</option>
        <option value="coin-days-destroyed">Coin Days Destroyed</option>
        <option value="coin-age-bands">HODL Age Bands</option>
        <option value="mean-coin-age">Mean Coin Age</option>
        <option value="total-coin-days">Total Coin Days</option>
      </optgroup>`
  }

  getMutilchainChartOptsHtml () {
    return `<optgroup label="Chain">
      <option value="block-size">Block Size</option>
      <option value="blockchain-size">Blockchain Size</option>
      <option value="tx-count">Transaction Count</option>
      <option value="tx-per-block">TXs Per Blocks</option>
      ${this.chainType === 'xmr' ? '<option value="duration-btw-blocks">Duration Between Blocks</option><option value="avg-tx-size">Average Tx Size</option>' : '<option value="address-number">Active Addresses</option>'}
      </optgroup>
      <optgroup label="Mining">
      <option value="pow-difficulty">Difficulty</option>
      <option value="hashrate">Hashrate</option>
      ${this.chainType === 'xmr'
? ''
: `<option value="mined-blocks">Mined Blocks</option>
      <option value="mempool-size">Mempool Size</option>
      <option value="mempool-txs">Mempool TXs</option>`}
      </optgroup>
      <optgroup label="Distribution">
      <option value="coin-supply">Coin Supply</option>
      <option value="fees">Fees</option>
      ${this.chainType === 'xmr' ? '<option value="fee-rate">Fee Rate</option>' : ''}
      </optgroup>
      ${this.chainType === 'xmr'
? `<optgroup label="Privacy">
                        <option value="total-ring-size">Total Ring Size</option>
                        <option value="avg-ring-size">Avg Ring Size / Input</option>
                        <option value="decoy-bands">Monero Decoys Bands</option>
                     </optgroup>`
: ''}
      `
  }

  async selectChart () {
    const selection = this.settings.chart = this.chartSelectTarget.value
    this.handlerChainChartHeaderLink()
    this.chartNameTarget.textContent = this.getChartName(this.chartSelectTarget.value)
    this.chartTitleNameTarget.textContent = this.chartNameTarget.textContent
    this.chartDescriptionTarget.dataset.ctooltip = this.getChartDescriptionTooltip(selection)
    this.customLimits = null
    this.chartWrapperTarget.classList.add('loading')
    if (isScaleDisabled(selection)) {
      this.scaleSelectorTarget.classList.add('d-hide')
    } else {
      this.scaleSelectorTarget.classList.remove('d-hide')
    }
    if (isModeEnabled(selection)) {
      this.modeSelectorTarget.classList.remove('d-hide')
    } else {
      this.modeSelectorTarget.classList.add('d-hide')
    }
    if (isBinDisabled(selection)) {
      this.settings.bin = 'day'
      this.setActiveToggleBtn(this.settings.bin, this.binButtons)
      this.binSelectorTarget.classList.add('d-hide')
    } else if (this.chainType === 'dcr' || this.chainType === 'xmr') {
      this.binSelectorTarget.classList.remove('d-hide')
    }
    if (hasMultipleVisibility(selection)) {
      this.vSelectorTarget.classList.remove('d-hide')
      this.updateVSelector(selection)
    } else {
      this.vSelectorTarget.classList.add('d-hide')
    }
    if (useRange(selection) && this.chainType === 'dcr') {
      this.settings.range = this.selectedRange()
      rangeOption = this.settings.range
      this.rangeSelectorTarget.classList.remove('d-hide')
    } else {
      this.settings.range = ''
      rangeOption = ''
      this.rangeSelectorTarget.classList.add('d-hide')
    }
    if (selectedChart !== selection || this.settings.bin !== this.selectedBin() ||
      this.settings.axis !== this.selectedAxis() || (this.chainType === 'dcr' && this.settings.range !== this.selectedRange())) {
      let url = this.chainType === 'dcr' ? '/api/chart/' + selection : `/api/chainchart/${this.chainType}/` + selection
      if (usesWindowUnits(selection) && !usesHybridUnits(selection)) {
        this.binSelectorTarget.classList.add('d-hide')
        this.settings.bin = 'window'
      } else if (!isBinDisabled(selection)) {
        this.settings.bin = this.selectedBin()
        const _this = this
        if (this.chainType === 'dcr' || this.chainType === 'xmr') {
          this.binSelectorTarget.classList.remove('d-hide')
          // handler for option window only
          this.binButtons.forEach(btn => {
            if (btn.name !== 'window') return
            if (usesHybridUnits(selection)) {
              btn.classList.remove('d-hide')
            } else {
              btn.classList.add('d-hide')
              if (_this.settings.bin === 'window') {
                _this.settings.bin = 'day'
                _this.setActiveToggleBtn(_this.settings.bin, _this.binButtons)
              }
            }
          })
          // if bin is blocks (xmr), hide option 'All' in zoom
          if (this.chainType === 'xmr' && this.settings.bin === 'block') {
            // if zoom is all, change to year
            const selectedZoom = this.zoomSelectorTarget.value
            // hide 'All' option
            const optionHtml = `<option value="year">Year</option>
                                <option value="month">Month</option>
                                <option value="week">Week</option>
                                <option value="day">Day</option>`
            this.zoomSelectorTarget.innerHTML = optionHtml
            if (!selectedZoom || selectedZoom === '' || selectedZoom === 'all') {
              this.settings.zoom = ''
              this.zoomSelectorTarget.value = 'year'
            } else {
              this.zoomSelectorTarget.value = selectedZoom
            }
          } else {
            const selectedZoom = this.zoomSelectorTarget.value
            // else, show 'All' option in zoom
            const optionHtml = `<option value="all">All</option>
                                <option value="year">Year</option>
                                <option value="month">Month</option>
                                <option value="week">Week</option>
                                <option value="day">Day</option>`
            this.zoomSelectorTarget.innerHTML = optionHtml
            this.zoomSelectorTarget.value = selectedZoom
          }
        } else {
          this.settings.bin = 'day'
          this.setActiveToggleBtn(this.settings.bin, this.binButtons)
          this.binSelectorTarget.classList.add('d-hide')
          const selectedZoom = this.zoomSelectorTarget.value
          // else, show 'All' option in zoom
          const optionHtml = `<option value="all">All</option>
                                <option value="year">Year</option>
                                <option value="month">Month</option>
                                <option value="week">Week</option>
                                <option value="day">Day</option>`
          this.zoomSelectorTarget.innerHTML = optionHtml
          this.zoomSelectorTarget.value = selectedZoom
        }
      }
      url += `?bin=${this.settings.bin}`
      if (this.chainType === 'dcr' && this.settings.range !== '') {
        url += `&range=${this.settings.range}`
      }
      this.settings.axis = this.selectedAxis()
      if (!this.settings.axis) this.settings.axis = 'time' // Set the default.
      url += `&axis=${this.settings.axis}`
      this.setActiveOptionBtn(this.settings.axis, this.axisOptionTargets)
      const chartResponse = await requestJSON(url)
      selectedChart = selection
      selectedType = this.chainType
      this.plotGraph(selection, chartResponse)
    } else {
      this.chartWrapperTarget.classList.remove('loading')
    }
  }

  getChartName (chartValue) {
    switch (chartValue) {
      case 'ticket-price':
        return 'Ticket Price'
      case 'ticket-pool-size':
        return 'Ticket Pool Size'
      case 'ticket-pool-value':
        return 'Ticket Pool Value'
      case 'stake-participation':
        return 'Stake Participation'
      case 'privacy-participation':
        return 'Privacy Participation'
      case 'missed-votes':
        return 'Missed Votes'
      case 'block-size':
        return 'Block Size'
      case 'blockchain-size':
        return 'Blockchain Size'
      case 'tx-count':
        return 'Transaction Count'
      case 'duration-btw-blocks':
        return 'Duration Between Blocks'
      case 'pow-difficulty':
        return 'PoW Difficulty'
      case 'chainwork':
        return 'Total Work'
      case 'hashrate':
        return 'Hashrate'
      case 'coin-supply':
        return 'Coin Supply'
      case 'fees':
        return 'Fees'
      case 'tx-per-block':
        return 'TXs Per Blocks'
      case 'mined-blocks':
        return 'Mined Blocks'
      case 'mempool-txs':
        return 'Mempool TXs'
      case 'mempool-size':
        return 'Mempool Size'
      case 'address-number':
        return 'Active Addresses'
      case 'avg-age-days':
        return 'Average Age Days'
      case 'coin-days-destroyed':
        return 'Coin Days Destroyed'
      case 'coin-age-bands':
        return 'HODL Age Bands'
      case 'mean-coin-age':
        return 'Mean Coin Age'
      case 'total-coin-days':
        return 'Total Coin Days'
      case 'total-ring-size':
        return 'Total Ring Size'
      case 'avg-ring-size':
        return 'Avg Ring Size / Input'
      case 'fee-rate':
        return 'Fee Rate'
      case 'avg-tx-size':
        return 'Average Tx Size'
      case 'decoy-bands':
        return 'Monero Decoys Bands'
      default:
        return ''
    }
  }

  async validateZoom () {
    await animationFrame()
    this.chartWrapperTarget.classList.add('loading')
    await animationFrame()
    let oldLimits = this.limits || this.chartsView.xAxisExtremes()
    this.limits = this.chartsView.xAxisExtremes()
    const selected = this.zoomSelectorTarget.value
    if (selected && this.isValidZoomSelect(selected) && !(selectedChart === 'privacy-participation' && selected === 'all')) {
      this.lastZoom = Zoom.validate(selected, this.limits,
        this.isTimeAxis() ? avgBlockTime : 1, this.isTimeAxis() ? 1 : avgBlockTime)
    } else {
      // if this is for the privacy-participation chart, then zoom to the beginning of the record
      if (selectedChart === 'privacy-participation') {
        this.limits = oldLimits = this.customLimits
        this.settings.zoom = Zoom.object(this.limits[0], this.limits[1])
      }
      this.lastZoom = Zoom.project(this.settings.zoom, oldLimits, this.limits)
    }
    if (this.lastZoom) {
      this.chartsView.updateOptions({
        dateWindow: [this.lastZoom.start, this.lastZoom.end]
      })
    }
    if (selected !== this.settings.zoom) {
      this._zoomCallback(this.lastZoom.start, this.lastZoom.end)
    }
    await animationFrame()
    this.chartWrapperTarget.classList.remove('loading')
    this.chartsView.updateOptions({
      zoomCallback: this.zoomCallback,
      drawCallback: this.drawCallback
    })
  }

  isValidZoomSelect (option) {
    return option === 'all' || option === 'year' || option === 'month' || option === 'week' || option === 'day'
  }

  defaultVisibilityFor (chart) {
    switch (chart) {
      case 'ticket-price': return 'true-false'
      case 'coin-supply': return 'true-true-false'
      case 'privacy-participation': return 'true-false'
      case 'avg-age-days':
      case 'coin-days-destroyed':
      case 'coin-age-bands':
      case 'mean-coin-age':
      case 'total-coin-days':
        return 'true-true-true'
      default: return null
    }
  }

  chartDefaultBin (chart) {
    if (isBinDisabled(chart)) return 'day'
    if (usesWindowUnits(chart) && !usesHybridUnits(chart)) return 'window'
    return this.defaultBin
  }

  filteredSettings () {
    const s = Object.assign({}, this.settings)
    if (s.chart === this.defaultChart) s.chart = null
    if (s.scale === this.defaultScale || s.scale === '') s.scale = null
    if (s.bin === this.chartDefaultBin(this.settings.chart)) s.bin = null
    if (s.range === this.defaultRange || s.range === '') s.range = null
    if (s.axis === this.defaultAxis) s.axis = null
    if (s.mode === this.defaultMode) s.mode = null
    if (s.visibility && s.visibility === this.defaultVisibilityFor(this.settings.chart)) s.visibility = null
    if (s.zoom && this.chartsView) {
      try {
        const ex = this.chartsView.xAxisExtremes()
        const isTime = this.settings.axis !== 'height'
        if (Zoom.mapKey(s.zoom, ex, isTime ? 1 : avgBlockTime) === 'all') s.zoom = null
      } catch (e) {}
    }
    return s
  }

  _zoomCallback (start, end) {
    this.lastZoom = Zoom.object(start, end)
    this.settings.zoom = Zoom.encode(this.lastZoom)
    if (!this.isHomepage) {
      this.query.replace(this.filteredSettings())
    }
    const ex = this.chartsView.xAxisExtremes()
    const option = Zoom.mapKey(this.settings.zoom, ex, this.isTimeAxis() ? 1 : avgBlockTime)
    this.zoomSelectorTarget.value = option
    const axesData = axesToRestoreYRange(this.settings.chart,
      this.supportedYRange, this.chartsView.yAxisRanges())
    if (axesData) this.chartsView.updateOptions({ axes: axesData })
  }

  isTimeAxis () {
    return this.selectedAxis() === 'time'
  }

  _drawCallback (graph, first) {
    // update position of y1, y2 label
    if (isMobile()) {
      // get axes
      const axes = graph.getOption('axes')
      if (axes) {
        const y1label = this.chartsViewTarget.querySelector('.dygraph-label.dygraph-ylabel')
        const y2label = this.chartsViewTarget.querySelector('.dygraph-label.dygraph-y2label')
        if (y1label) {
          const yAxis = axes.y
          if (yAxis) {
            const yLabelWidth = yAxis.axisLabelWidth
            if (yLabelWidth) {
              y1label.style.top = (Number(yLabelWidth) + 5) + 'px'
            }
          }
        }
        if (y2label) {
          const y2Axis = axes.y2
          if (y2Axis) {
            const y2LabelWidth = y2Axis.axisLabelWidth
            if (y2LabelWidth) {
              y2label.style.top = (Number(y2LabelWidth) + 5) + 'px'
            }
          }
        }
      }
    }
    if (first) return
    const [start, end] = this.chartsView.xAxisRange()
    if (start === end) return
    if (this.lastZoom.start === start) return // only handle slide event.
    this._zoomCallback(start, end)
  }

  setInitSelectZoom (e) {
    const ex = this.chartsView.xAxisExtremes()
    const option = Zoom.mapKey(e, ex, this.isTimeAxis() ? 1 : avgBlockTime)
    this.zoomSelectorTarget.value = option
    if (!e.target) return // Exit if running for the first time\
    this.validateZoom()
  }

  setSelectZoom (e) {
    this.validateZoom()
  }

  setSelectBin (e) {
    const btn = e.target || e.srcElement
    if (!btn) {
      return
    }
    let option
    if (btn.nodeName === 'BUTTON') {
      option = btn.name
    }
    if (!option) {
      return
    }
    this.setActiveToggleBtn(option, this.binButtons)
    this.updateVSelector()
    selectedChart = null // Force fetch
    this.selectChart()
  }

  setSelectScale (e) {
    const btn = e.target || e.srcElement
    if (!btn) {
      return
    }
    let option
    if (btn.nodeName === 'BUTTON') {
      option = btn.name
    }
    if (!option) {
      return
    }
    this.setActiveToggleBtn(option, this.scaleButtons)
    const optionUpdate = { logscale: option === 'log' }
    if (this.chainType === 'dcr' && this.settings.range !== 'after') {
      if (option === 'log') {
        optionUpdate.plotter = null
      } else {
        if (this.settings.chart === 'pow-difficulty') {
          optionUpdate.plotter = this.settings.axis === 'height' ? difficultyBlockPlotter : difficultyTimePlotter
        } else if (this.settings.chart === 'hashrate') {
          optionUpdate.plotter = this.settings.axis === 'height' ? hashrateBlockPlotter : hashrateTimePlotter
        }
      }
    }
    if (this.chartsView) {
      this.chartsView.updateOptions(optionUpdate)
    }
    this.settings.scale = option
    if (!this.isHomepage) {
      this.query.replace(this.filteredSettings())
    }
  }

  setSelectMode (e) {
    const btn = e.target || e.srcElement
    if (!btn) {
      return
    }
    let option
    if (btn.nodeName === 'BUTTON') {
      option = btn.name
    }
    if (!option) {
      return
    }
    this.setActiveToggleBtn(option, this.modeButtons)
    if (this.chartsView) {
      this.chartsView.updateOptions({ stepPlot: option === 'stepped' })
    }
    this.settings.mode = option
    if (!this.isHomepage) {
      this.query.replace(this.filteredSettings())
    }
  }

  setAxis (e) {
    const target = e.srcElement || e.target
    const option = target ? target.dataset.option : e
    if (!option) return
    this.setActiveOptionBtn(option, this.axisOptionTargets)
    if (!target) return // Exit if running for the first time.
    this.settings.axis = null
    this.selectChart()
  }

  setVisibilityFromSettings () {
    switch (this.chartSelectTarget.value) {
      case 'ticket-price':
        if (this.visibility.length !== 2) {
          this.visibility = [true, this.ticketsPurchaseTarget.checked]
        }
        this.ticketsPriceTarget.checked = this.visibility[0]
        this.ticketsPurchaseTarget.checked = this.visibility[1]
        break
      case 'coin-supply':
        if (this.visibility.length !== 3) {
          this.visibility = [true, true, this.anonymitySetTarget.checked]
        }
        this.anonymitySetTarget.checked = this.visibility[2]
        break
      case 'privacy-participation':
        if (this.visibility.length !== 2) {
          this.visibility = [true, this.anonymitySetTarget.checked]
        }
        this.anonymitySetTarget.checked = this.visibility[1]
        break
      case 'avg-age-days':
      case 'coin-days-destroyed':
      case 'coin-age-bands':
      case 'mean-coin-age':
      case 'total-coin-days':
        if (this.visibility.length !== 3) {
          this.visibility = [true, true, this.marketPriceTarget.checked]
        }
        this.marketPriceTarget.checked = this.visibility[2]
        break
      default:
        return
    }
    this.settings.visibility = this.visibility.join('-')
    if (!this.isHomepage) {
      this.query.replace(this.filteredSettings())
    }
  }

  setActiveOptionBtn (opt, optTargets) {
    optTargets.forEach(li => {
      if (li.dataset.option === opt) {
        li.classList.add('active')
      } else {
        li.classList.remove('active')
      }
    })
  }

  setActiveToggleBtn (opt, optTargets) {
    optTargets.forEach(button => {
      if (button.name === opt) {
        button.classList.add('active')
      } else {
        button.classList.remove('active')
      }
    })
  }

  hideToggleBtn (opt, optTargets) {
    optTargets.forEach(button => {
      if (button.name === opt) {
        button.classList.add('d-hide')
      }
    })
  }

  showToggleBtn (opt, optTargets) {
    optTargets.forEach(button => {
      if (button.name === opt) {
        button.classList.remove('d-hide')
      }
    })
  }

  selectedBin () { return this.selectedButtons(this.binButtons) }
  selectedScale () { return this.selectedButtons(this.scaleButtons) }
  selectedAxis () { return this.selectedOption(this.axisOptionTargets) }
  selectedRange () { return this.selectedOption(this.rangeOptionTargets) }

  selectedOption (optTargets) {
    let key = false
    optTargets.forEach((el) => {
      if (el.classList.contains('active')) key = el.dataset.option
    })
    return key
  }

  selectedButtons (btnTargets) {
    let key = false
    btnTargets.forEach((btn) => {
      if (btn.classList.contains('active')) key = btn.name
    })
    return key
  }
}
