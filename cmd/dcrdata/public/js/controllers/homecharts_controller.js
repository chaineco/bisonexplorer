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
const windowScales = ['ticket-price', 'pow-difficulty', 'missed-votes']
const hybridScales = ['privacy-participation']
const lineScales = ['ticket-price', 'privacy-participation']
const modeScales = ['ticket-price']
const multiYAxisChart = ['ticket-price', 'coin-supply', 'privacy-participation']
const decredChartOpts = ['ticket-price', 'ticket-pool-size', 'ticket-pool-value', 'stake-participation',
  'privacy-participation', 'missed-votes', 'block-size', 'blockchain-size', 'tx-count', 'duration-btw-blocks',
  'pow-difficulty', 'chainwork', 'hashrate', 'coin-supply', 'fees']
const mutilchainChartOpts = ['block-size', 'blockchain-size', 'tx-count', 'tx-per-block', 'address-number',
  'pow-difficulty', 'hashrate', 'mined-blocks', 'mempool-size', 'mempool-txs', 'coin-supply', 'fees']
let globalChainType = ''
// index 0 represents y1 and 1 represents y2 axes.
const yValueRanges = { 'ticket-price': [1] }
const hashrateUnits = ['Th/s', 'Ph/s', 'Eh/s']
let ticketPoolSizeTarget, premine, stakeValHeight, stakeShare
let baseSubsidy, subsidyInterval, subsidyExponent, windowSize, avgBlockTime
let rawCoinSupply, rawPoolValue
let yFormatter, legendEntry, legendMarker, legendElement

function usesWindowUnits (chart) {
  return windowScales.indexOf(chart) > -1
}

function usesHybridUnits (chart) {
  return hybridScales.indexOf(chart) > -1
}

function isScaleDisabled (chart) {
  return lineScales.indexOf(chart) > -1
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
  addLegendEntry(div, data.series[0])
}

function customYFormatter (fmt) {
  return (div, data) => addLegendEntryFmt(div, data.series[0], fmt)
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

function powDiffFunc (data) {
  if (data.t) return zipWindowTvY(data.t, data.diff)
  return zipWindowHvY(data.diff, data.window)
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
    return [xFunc(i), supplies[i] * atomsToDCR, null, 0]
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
      'ticketsPrice'
    ]
  }

  async connect () {
    this.isHomepage = !window.location.href.includes('/charts')
    this.query = new TurboQuery()
    premine = parseInt(this.data.get('premine'))
    stakeValHeight = parseInt(this.data.get('svh'))
    stakeShare = parseInt(this.data.get('pos')) / 10.0
    baseSubsidy = parseInt(this.data.get('bs'))
    subsidyInterval = parseInt(this.data.get('sri'))
    subsidyExponent = parseFloat(this.data.get('mulSubsidy')) / parseFloat(this.data.get('divSubsidy'))
    this.chainType = 'dcr'
    avgBlockTime = parseInt(this.data.get(this.chainType + 'BlockTime')) * 1000
    globalChainType = this.chainType
    legendElement = this.labelsTarget

    // Prepare the legend element generators.
    const lm = this.legendMarkerTarget
    lm.remove()
    lm.removeAttribute('data-charts-target')
    legendMarker = () => {
      const node = document.createElement('div')
      node.appendChild(lm.cloneNode())
      return node.innerHTML
    }
    const le = this.legendEntryTarget
    le.remove()
    le.removeAttribute('data-charts-target')
    legendEntry = s => {
      const node = le.cloneNode()
      node.innerHTML = s
      return node
    }

    this.settings = TurboQuery.nullTemplate(['chart', 'zoom', 'scale', 'bin', 'axis', 'visibility', 'home'])
    if (!this.isHomepage) {
      this.query.update(this.settings)
    }
    this.settings.chart = this.settings.chart || 'ticket-price'
    this.zoomButtons = this.zoomSelectorTarget.querySelectorAll('button')
    this.binButtons = this.binSelectorTarget.querySelectorAll('button')
    this.scaleButtons = this.scaleSelectorTarget.querySelectorAll('button')
    this.zoomCallback = this._zoomCallback.bind(this)
    this.drawCallback = this._drawCallback.bind(this)
    this.limits = null
    this.lastZoom = null
    this.visibility = []
    if (this.settings.visibility) {
      this.settings.visibility.split('-', -1).forEach(s => {
        this.visibility.push(s === 'true')
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
      default:
        return
    }
    this.chartsView.updateOptions({ visibility: this.visibility })
    this.settings.visibility = this.visibility.join('-')
    if (!this.isHomepage) {
      this.query.replace(this.settings)
    }
  }

  disconnect () {
    globalEventBus.off('NIGHT_MODE', this.processNightMode)
    if (this.chartsView !== undefined) {
      this.chartsView.destroy()
    }
    selectedChart = null
  }

  drawInitialGraph () {
    const options = {
      axes: { y: { axisLabelWidth: 70 }, y2: { axisLabelWidth: 65 } },
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
      axisLineColor: '#C4CBD2'
    }

    this.chartsView = new Dygraph(
      this.chartsViewTarget,
      [[1, 1, 5], [2, 5, 11]],
      options
    )
    this.chartSelectTarget.value = this.settings.chart

    if (this.settings.axis) this.setAxis(this.settings.axis) // set first
    if (this.settings.scale === 'log') this.setSelectScale(this.settings.scale)
    if (this.settings.zoom) this.setSelectZoom(this.settings.zoom)
    this.setSelectBin(this.settings.bin ? this.settings.bin : 'day')
    this.setMode(this.settings.mode ? this.settings.mode : 'smooth')

    const ogLegendGenerator = Dygraph.Plugins.Legend.generateLegendHTML
    Dygraph.Plugins.Legend.generateLegendHTML = (g, x, pts, w, row) => {
      g.updateOptions({ legendIndex: row }, true)
      return ogLegendGenerator(g, x, pts, w, row)
    }
    this.selectChart()
  }

  plotGraph (chartName, data) {
    let d = []
    const gOptions = {
      zoomCallback: null,
      drawCallback: null,
      logscale: this.settings.scale === 'log',
      valueRange: [null, null],
      visibility: null,
      y2label: null,
      y3label: null,
      stepPlot: this.settings.mode === 'stepped',
      axes: {},
      series: null,
      inflation: null
    }
    rawPoolValue = []
    rawCoinSupply = []
    yFormatter = defaultYFormatter
    const xlabel = data.t ? 'Date' : 'Block Height'

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
          axisLabelFormatter: (y) => Math.round(y)
        }
        yFormatter = customYFormatter(y => y.toFixed(8) + ' DCR')
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
        yFormatter = customYFormatter(y => `${intComma(y)} tickets &nbsp;&nbsp; (network target ${intComma(ticketPoolSizeTarget)})`)
        break
      case 'stake-participation':
        d = percentStakedFunc(data)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Stake Participation'], true,
          'Stake Participation (%)', true, false))
        yFormatter = (div, data, i) => {
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
            addLegendEntryFmt(div, data.series[0], y => y > 0 ? intComma(y) : '0' + ' DCR')
          }
          break
        }
      case 'ticket-pool-value': // pool value graph
        d = zip2D(data, data.poolval, atomsToDCR)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Ticket Pool Value'], true,
          'Ticket Pool Value (DCR)', true, false))
        yFormatter = customYFormatter(y => intComma(y) + ' DCR')
        break
      case 'block-size': // block size graph
        d = zip2D(data, data.size)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Block Size'], false, 'Block Size', true, false))
        break
      case 'blockchain-size': // blockchain size graph
        d = zip2D(data, data.size)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Blockchain Size'], true,
          'Blockchain Size', false, true))
        break
      case 'tx-count': // tx per block graph
        d = zip2D(data, data.count)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Total Transactions'], false,
          'Total Transactions', false, false))
        break
      case 'tx-per-block': // tx per block graph
        d = zip2D(data, data.count)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Avg TXs Per Block'], false,
          'Avg TXs Per Block', false, false))
        break
      case 'mined-blocks': // tx per block graph
        d = zip2D(data, data.count)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Mined Blocks'], false,
          'Mined Blocks', false, false))
        break
      case 'mempool-txs': // tx per block graph
        d = zip2D(data, data.count)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Mempool Transactions'], false,
          'Mempool Transactions', false, false))
        break
      case 'mempool-size': // blockchain size graph
        d = zip2D(data, data.size)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Mempool Size'], true,
          'Mempool Size', false, true))
        break
      case 'address-number': // tx per block graph
        d = zip2D(data, data.count)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Active Addresses'], false,
          'Active Addresses', false, false))
        break
      case 'pow-difficulty': // difficulty graph
        d = powDiffFunc(data)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Difficulty'], true, 'Difficulty', true, false))
        break
      case 'coin-supply': // supply graph
        if (this.settings.bin === 'day') {
          d = zip2D(data, data.supply)
          assign(gOptions, mapDygraphOptions(d, [xlabel, 'Coins Supply'], false,
            'Coins Supply', false, false))
          break
        }
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
          addLegendEntryFmt(div, data.series[0], y => intComma(y) + ' ' + globalChainType.toUpperCase())
          let change = 0
          if (i < d.inflation.length) {
            if (selectedType === 'dcr') {
              const supply = data.series[0].y
              if (this.anonymitySetTarget.checked) {
                const mixed = data.series[2].y
                const mixedPercentage = ((mixed / supply) * 100).toFixed(2)
                div.appendChild(legendEntry(`${legendMarker()} Mixed: ${intComma(mixed)} DCR (${mixedPercentage}%)`))
              }
            }
            const predicted = d.inflation[i]
            const unminted = predicted - data.series[0].y
            change = ((unminted / predicted) * 100).toFixed(2)
            div.appendChild(legendEntry(`${legendMarker()} Unminted: ${intComma(unminted)} ` + globalChainType.toUpperCase() + ` (${change}%)`))
          }
        }
        break

      case 'fees': // block fee graph
        d = zip2D(data, data.fees, atomsToDCR)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Total Fee'], false, 'Total Fee (' + globalChainType.toUpperCase() + ')', true, false))
        break

      case 'duration-btw-blocks': // Duration between blocks graph
        d = zip2D(data, data.duration, 1, 1)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Duration Between Blocks'], false,
          'Duration Between Blocks (seconds)', false, false))
        break

      case 'hashrate': // Total chainwork over time
        d = zip2D(data, data.rate, 1e-3, data.offset)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Network Hashrate (petahash/s)'],
          false, 'Network Hashrate (petahash/s)', true, false))
        yFormatter = customYFormatter(y => withBigUnits(y * 1e3, hashrateUnits))
        break
    }

    const baseURL = `${this.query.url.protocol}//${this.query.url.host}`
    this.rawDataURLTarget.textContent = this.chainType === 'dcr' ? `${baseURL}/api/chart/${chartName}?axis=${this.settings.axis}&bin=${this.settings.bin}` :
     `${baseURL}/api/chainchart/${this.chainType}/${chartName}?axis=${this.settings.axis}&bin=${this.settings.bin}`

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

  async chainTypeChange (e) {
    if (e.target.name === this.chainType) {
      return
    }
    const target = e.srcElement || e.target
    this.chainTypeSelectedTargets.forEach((cTypeTarget) => {
      cTypeTarget.classList.remove('active')
    })
    target.classList.add('active')
    let reinitChartType = false
    reinitChartType = this.chainType === 'dcr' || e.target.name === 'dcr'
    this.chainType = e.target.name
    // reinit
    if (reinitChartType) {
      this.chartSelectTarget.innerHTML = this.chainType === 'dcr' ? this.getDecredChartOptsHtml() : this.getMutilchainChartOptsHtml()
    }
    // determind select chart
    this.settings.chart = this.getSelectedChart()
    this.chartNameTarget.textContent = this.getChartName(this.chartSelectTarget.value)
    this.chartTitleNameTarget.textContent = this.chartNameTarget.textContent
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
    if (hasMultipleVisibility(this.settings.chart)) {
      this.vSelectorTarget.classList.remove('d-hide')
      this.updateVSelector(this.settings.chart)
    } else {
      this.vSelectorTarget.classList.add('d-hide')
    }
    if (selectedType !== this.chainType || (selectedType === this.chainType && selectedChart !== this.settings.chart) ||
    this.settings.bin !== this.selectedBin() || this.settings.axis !== this.selectedAxis()) {
      let url = this.chainType === 'dcr' ? '/api/chart/' + this.settings.chart : `/api/chainchart/${this.chainType}/` + this.settings.chart
      if (usesWindowUnits(this.settings.chart) && !usesHybridUnits(this.settings.chart)) {
        this.binSelectorTarget.classList.add('d-hide')
        this.settings.bin = 'window'
      } else {
        this.settings.bin = this.selectedBin()
        if (this.chainType === 'dcr') {
          this.binSelectorTarget.classList.remove('d-hide')
          this.binButtons.forEach(btn => {
            if (btn.name !== 'window') return
            if (usesHybridUnits(this.settings.chart)) {
              btn.classList.remove('d-hide')
            } else {
              btn.classList.add('d-hide')
              if (this.settings.bin === 'window') {
                this.settings.bin = 'day'
                this.setActiveToggleBtn(this.settings.bin, this.binButtons)
              }
            }
          })
        } else {
          this.binSelectorTarget.classList.add('d-hide')
        }
      }
      url += `?bin=${this.settings.bin}`
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

  getSelectedChart () {
    let hasChart = false
    const chartOpts = this.chainType === 'dcr' ? decredChartOpts : mutilchainChartOpts
    chartOpts.forEach((opt) => {
      if (this.settings.chart === opt) {
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
    return '<optgroup label="Staking">' +
    '<option value="ticket-price">Ticket Price</option>' +
    '<option value="ticket-pool-size">Ticket Pool Size</option>' +
    '<option value="ticket-pool-value">Ticket Pool Value</option>' +
    '<option value="stake-participation">Stake Participation</option>' +
    '<option value="privacy-participation">Privacy Participation</option>' +
    '<option value="missed-votes">Missed Votes</option>' +
    '</optgroup>' +
    '<optgroup label="Chain">' +
    '<option value="block-size">Block Size</option>' +
    '<option value="blockchain-size">Blockchain Size</option>' +
    '<option value="tx-count">Transaction Count</option>' +
    '<option value="duration-btw-blocks">Duration Between Blocks</option>' +
    '</optgroup>' +
    '<optgroup label="Mining">' +
    '<option value="pow-difficulty">PoW Difficulty</option>' +
    '<option value="chainwork">Total Work</option>' +
    '<option value="hashrate">Hashrate</option>' +
    '</optgroup>' +
    '<optgroup label="Distribution">' +
    '<option value="coin-supply">Circulation</option>' +
    '<option value="fees">Fees</option>' +
    '</optgroup>'
  }

  getMutilchainChartOptsHtml () {
    return '<optgroup label="Chain">' +
    '<option value="block-size">Block Size</option>' +
    '<option value="blockchain-size">Blockchain Size</option>' +
    '<option value="tx-count">Transaction Count</option>' +
    '<option value="tx-per-block">TXs Per Blocks</option>' +
    '<option value="address-number">Active Addresses</option>' +
    '</optgroup>' +
    '<optgroup label="Mining">' +
    '<option value="pow-difficulty">Difficulty</option>' +
    '<option value="hashrate">Hashrate</option>' +
    '<option value="mined-blocks">Mined Blocks</option>' +
    '<option value="mempool-size">Mempool Size</option>' +
    '<option value="mempool-txs">Mempool TXs</option>' +
    '</optgroup>' +
    '<optgroup label="Distribution">' +
    '<option value="coin-supply">Circulation</option>' +
    '<option value="fees">Fees</option>' +
    '</optgroup>'
  }

  async selectChart () {
    const selection = this.settings.chart = this.chartSelectTarget.value
    this.chartNameTarget.textContent = this.getChartName(this.chartSelectTarget.value)
    this.chartTitleNameTarget.textContent = this.chartNameTarget.textContent
    this.customLimits = null
    this.chartWrapperTarget.classList.add('loading')
    if (isScaleDisabled(selection)) {
      this.scaleSelectorTarget.classList.add('d-hide')
      this.vSelectorTarget.classList.remove('d-hide')
    } else {
      this.scaleSelectorTarget.classList.remove('d-hide')
    }
    if (isModeEnabled(selection)) {
      this.modeSelectorTarget.classList.remove('d-hide')
    } else {
      this.modeSelectorTarget.classList.add('d-hide')
    }
    if (hasMultipleVisibility(selection)) {
      this.vSelectorTarget.classList.remove('d-hide')
      this.updateVSelector(selection)
    } else {
      this.vSelectorTarget.classList.add('d-hide')
    }
    if (selectedChart !== selection || this.settings.bin !== this.selectedBin() ||
      this.settings.axis !== this.selectedAxis()) {
      let url = this.chainType === 'dcr' ? '/api/chart/' + selection : `/api/chainchart/${this.chainType}/` + selection
      if (usesWindowUnits(selection) && !usesHybridUnits(selection)) {
        this.binSelectorTarget.classList.add('d-hide')
        this.settings.bin = 'window'
      } else {
        this.settings.bin = this.selectedBin()
        if (this.chainType === 'dcr') {
          this.binSelectorTarget.classList.remove('d-hide')
          this.binButtons.forEach(btn => {
            if (btn.name !== 'window') return
            if (usesHybridUnits(selection)) {
              btn.classList.remove('d-hide')
            } else {
              btn.classList.add('d-hide')
              if (this.settings.bin === 'window') {
                this.settings.bin = 'day'
                this.setActiveToggleBtn(this.settings.bin, this.binButtons)
              }
            }
          })
        } else {
          this.binSelectorTarget.classList.add('d-hide')
        }
      }
      url += `?bin=${this.settings.bin}`
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
      case 'hashrate':
        return 'Hashrate'
      case 'coin-supply':
        return 'Circulation'
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
    const selected = this.selectedZoom()
    if (selected && !(selectedChart === 'privacy-participation' && selected === 'all')) {
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

  _zoomCallback (start, end) {
    this.lastZoom = Zoom.object(start, end)
    this.settings.zoom = Zoom.encode(this.lastZoom)
    if (!this.isHomepage) {
      this.query.replace(this.settings)
    }
    const ex = this.chartsView.xAxisExtremes()
    const option = Zoom.mapKey(this.settings.zoom, ex, this.isTimeAxis() ? 1 : avgBlockTime)
    this.setActiveToggleBtn(option, this.zoomButtons)
    const axesData = axesToRestoreYRange(this.settings.chart,
      this.supportedYRange, this.chartsView.yAxisRanges())
    if (axesData) this.chartsView.updateOptions({ axes: axesData })
  }

  isTimeAxis () {
    return this.selectedAxis() === 'time'
  }

  _drawCallback (graph, first) {
    if (first) return
    const [start, end] = this.chartsView.xAxisRange()
    if (start === end) return
    if (this.lastZoom.start === start) return // only handle slide event.
    this._zoomCallback(start, end)
  }

  setSelectZoom (e) {
    const btn = e.target || e.srcElement
    let option
    if (btn.nodeName === 'BUTTON') {
      option = btn.name
    } else {
      const ex = this.chartsView.xAxisExtremes()
      option = Zoom.mapKey(e, ex, this.isTimeAxis() ? 1 : avgBlockTime)
    }
    this.setActiveToggleBtn(option, this.zoomButtons)
    if (!e.target) return // Exit if running for the first time\
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
    if (this.chartsView) {
      this.chartsView.updateOptions({ logscale: option === 'log' })
    }
    this.settings.scale = option
    if (!this.isHomepage) {
      this.query.replace(this.settings)
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
    this.setActiveOptionBtn(option, this.modeOptionTargets)
    if (this.chartsView) {
      this.chartsView.updateOptions({ stepPlot: option === 'stepped' })
    }
    this.settings.mode = option
    if (!this.isHomepage) {
      this.query.replace(this.settings)
    }
  }

  setMode (e) {
    const target = e.srcElement || e.target
    const option = target ? target.dataset.option : e
    if (!option) return
    this.setActiveOptionBtn(option, this.modeOptionTargets)
    if (!target) return // Exit if running for the first time.
    if (this.chartsView) {
      this.chartsView.updateOptions({ stepPlot: option === 'stepped' })
    }
    this.settings.mode = option
    if (!this.isHomepage) {
      this.query.replace(this.settings)
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
      default:
        return
    }
    this.settings.visibility = this.visibility.join('-')
    if (!this.isHomepage) {
      this.query.replace(this.settings)
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

  selectedZoom () { return this.selectedButtons(this.zoomButtons) }
  selectedBin () { return this.selectedButtons(this.binButtons) }
  selectedScale () { return this.selectedButtons(this.scaleButtons) }
  selectedAxis () { return this.selectedOption(this.axisOptionTargets) }

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
