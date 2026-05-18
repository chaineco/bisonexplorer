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
let Dygraph // lazy loaded on connect

const aDay = 86400 * 1000 // in milliseconds
const aMonth = 30 // in days
const atomsToDCR = 1e-8
const picoToXmr = 1e-12
const windowScales = ['ticket-price', 'missed-votes']
const hybridScales = ['privacy-participation']
const lineScales = ['ticket-price', 'privacy-participation', 'decoy-bands']
const modeScales = ['ticket-price']
const binDisabled = ['decoy-bands']
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
let globalChainType = ''
// index 0 represents y1 and 1 represents y2 axes.
const yValueRanges = { 'ticket-price': [1] }
const hashrateUnits = ['H/s', 'KH/s', 'MH/s', 'GH/s', 'TH/s', 'PH/s', 'EH/s']
let premine, stakeValHeight, stakeShare
let baseSubsidy, subsidyInterval, subsidyExponent, avgBlockTime
let yFormatter, legendEntry, legendMarker, legendElement
const yAxisLabelWidth = {
  y1: {
    'block-size': 50,
    'blockchain-size': 50,
    'tx-count': 45,
    'tx-per-block': 50,
    'duration-btw-blocks': 40,
    'address-number': 45,
    'pow-difficulty': 50,
    hashrate: 50,
    'mined-blocks': 40,
    'mempool-size': 40,
    'mempool-txs': 50,
    'coin-supply': 40,
    'total-ring-size': 40,
    'avg-ring-size': 40,
    'fee-rate': 50,
    'avg-tx-size': 40,
    fees: 50,
    'decoy-bands': 40
  },
  y2: {
    'decoy-bands': 50
  }
}

function unitToCoin (chainType) {
  if (chainType === 'xmr') {
    return picoToXmr
  }
  return atomsToDCR
}

function isMobile () {
  return window.innerWidth <= 768
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

function intComma (amount) {
  if (!amount) return ''
  return amount.toLocaleString(undefined, { maximumFractionDigits: 0 })
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

function circulationFunc (chartData, chainType) {
  let yMax = 0
  let h = -1
  const addDough = (newHeight) => {
    while (h < newHeight) {
      h++
      yMax += blockReward(h) * unitToCoin(chainType)
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
    return [xFunc(i), supplies[i] * unitToCoin(chainType), null, 0]
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

// handler func for decoy bands
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
      'chartDescription'
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
    avgBlockTime = parseInt(this.data.get('blockTime')) * 1000
    this.chainType = this.data.get('chainType')
    this.supplyPage = this.data.get('supplyPage')
    globalChainType = this.chainType
    legendElement = this.labelsTarget
    if (this.chainType === 'xmr') {
      $('#chainChartFooterRow').removeClass('d-hide')
    } else {
      $('#chainChartFooterRow').addClass('d-hide')
    }
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

    this.settings = TurboQuery.nullTemplate(['chart', 'zoom', 'scale', 'bin', 'axis', 'visibility', 'home', 'mode'])
    if (!this.isHomepage) {
      this.query.update(this.settings)
    }
    this.settings.chart = this.settings.chart || 'block-size'
    if (this.supplyPage === 'true') {
      this.settings.chart = 'coin-supply'
    }
    this.defaultChart = this.supplyPage === 'true' ? 'coin-supply' : 'block-size'
    this.defaultScale = 'linear'
    this.defaultBin = 'day'
    this.defaultAxis = 'time'
    this.defaultMode = 'smooth'
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
    const _this = this
    // add space to chart selector
    this.chartSelectTargets.forEach((chartSelector) => {
      if (!_this.isHomepage || isMobile()) {
        const currentWidth = parseInt(window.getComputedStyle(chartSelector).width, 10)
        chartSelector.style.width = (currentWidth + 25) + 'px'
      }
    })
    globalEventBus.on('NIGHT_MODE', this.processNightMode)
    this._onDocClick = this.onDocClick.bind(this)
    this._onKeydown = this.onKeydown.bind(this)
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
            const props = g.getPropertiesForSeries(p.name) // get corectly axis & column
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

        // Flip horizontally if left edge touched (with viewport)
        if (left + rect.left < 0) {
          left = relX + 10
        }
        // Flip horizontally if right edge touched (with viewport)
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
    this.setSelectedChart(this.settings.chart)

    if (this.settings.axis) this.setAxis(this.settings.axis) // set first
    if (this.settings.scale === 'log') this.setScale(this.settings.scale)
    // default on mobile is year, on other is all
    if (humanize.isEmpty(this.settings.zoom) && isMobile() && this.supplyPage !== 'true') {
      this.settings.zoom = 'year'
    }
    if (this.settings.zoom) this.setZoom(this.settings.zoom)
    this.setBin(this.settings.bin ? this.settings.bin : 'day')
    this.setMode(this.settings.mode ? this.settings.mode : 'smooth')

    const ogLegendGenerator = Dygraph.Plugins.Legend.generateLegendHTML
    Dygraph.Plugins.Legend.generateLegendHTML = (g, x, pts, w, row) => {
      g.updateOptions({ legendIndex: row }, true)
      return ogLegendGenerator(g, x, pts, w, row)
    }
    this.selectChart()
  }

  chainChartHeaderClick (e) {
    if (e.target.closest('[data-chaincharts-target="chartDescription"]')) {
      e.preventDefault()
    }
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
      stepPlot: this.settings.mode === 'stepped',
      axes: {},
      series: null,
      inflation: null,
      fillGraph: false,
      stackedGraph: false
    }

    yFormatter = defaultYFormatter
    const xlabel = data.t ? 'Date' : 'Block Height'
    const labels = []
    const stackVisibility = []
    switch (chartName) {
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
          'Mined Blocks', false, false))
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
        d = zip2D(data, data.diff)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Difficulty'], true, 'Difficulty', true, false))
        break
      case 'coin-supply': // supply graph
        if (this.chainType !== 'dcr') {
          d = zip2D(data, data.supply)
          assign(gOptions, mapDygraphOptions(d, [xlabel, 'Coins Supply (' + globalChainType.toUpperCase() + ')'], true,
            'Coins Supply (' + globalChainType.toUpperCase() + ')', true, false))
          yFormatter = (div, data, i) => {
            addLegendEntryFmt(div, data.series[0], y => intComma(y) + ' ' + globalChainType.toUpperCase())
          }
          break
        }
        d = circulationFunc(data, this.chainType)
        assign(gOptions, mapDygraphOptions(d.data, [xlabel, 'Coin Supply', 'Inflation Limit', 'Mix Rate'],
          true, 'Coin Supply (' + this.chainType.toUpperCase() + ')', true, false))
        gOptions.y2label = 'Inflation Limit'
        gOptions.y3label = 'Mix Rate'
        gOptions.series = { 'Inflation Limit': { axis: 'y2' }, 'Mix Rate': { axis: 'y3' } }
        this.visibility = [true, true, false]
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
            const predicted = d.inflation[i]
            const unminted = predicted - data.series[0].y
            change = ((unminted / predicted) * 100).toFixed(2)
            div.appendChild(legendEntry(`${legendMarker()} Unminted: ${intComma(unminted)} ` + globalChainType.toUpperCase() + ` (${change}%)`))
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
        d = zip2D(data, data.rate, 1, data.offset)
        assign(gOptions, mapDygraphOptions(d, [xlabel, 'Network Hashrate'],
          false, 'Network Hashrate', false, true))
        yFormatter = customYFormatter(y => withBigUnits(y, hashrateUnits))
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
      axisLabelWidth: isMobile() ? yAxisLabelWidth.y1[chartName] : yAxisLabelWidth.y1[chartName] + 5
    }
    const baseURL = `${this.query.url.protocol}//${this.query.url.host}`
    this.rawDataURLTarget.textContent = `${baseURL}/api/chainchart/${this.chainType}/${chartName}?axis=${this.settings.axis}&bin=${this.settings.bin}`

    this.chartsView.plotter_.clear()
    this.chartsView.updateOptions(gOptions, false)
    if (yValueRanges[chartName]) this.supportedYRange = this.chartsView.yAxisRanges()
    this.validateZoom()
  }

  async selectChart () {
    const selectChart = this.getSelectedChart()
    const selection = this.settings.chart = selectChart
    this.chartNameTarget.textContent = this.getChartName(selectChart)
    this.chartTitleNameTarget.textContent = this.chartNameTarget.textContent
    this.chartDescriptionTarget.dataset.ctooltip = this.getChartDescriptionTooltip(selection)
    this.customLimits = null
    this.chartWrapperTarget.classList.add('loading')
    if (isScaleDisabled(selection)) {
      this.hideMultiTargets(this.scaleSelectorTargets)
    } else {
      this.showMultiTargets(this.scaleSelectorTargets)
    }
    if (isModeEnabled(selection)) {
      this.showMultiTargets(this.modeSelectorTargets)
    } else {
      this.hideMultiTargets(this.modeSelectorTargets)
    }

    if (isBinDisabled(selection)) {
      this.settings.bin = 'day'
      this.setActiveOptionBtn(this.settings.bin, this.binSizeTargets)
      this.hideMultiTargets(this.binSelectorTargets)
    } else if (this.chainType === 'xmr') {
      this.showMultiTargets(this.binSelectorTargets)
    }

    if (selectedChart !== selection || this.settings.bin !== this.selectedBin() ||
      this.settings.axis !== this.selectedAxis()) {
      let url = `/api/chainchart/${this.chainType}/` + selection
      if (usesWindowUnits(selection) && !usesHybridUnits(selection)) {
        // this.binSelectorTarget.classList.add('d-hide')
        this.hideMultiTargets(this.binSelectorTargets)
        this.settings.bin = 'window'
      } else if (!isBinDisabled(selection)) {
        if (this.chainType === 'xmr') {
        // this.binSelectorTarget.classList.remove('d-hide')
          this.showMultiTargets(this.binSelectorTargets)
          this.settings.bin = this.selectedBin()
          const _this = this
          // handler for option window only
          this.binSizeTargets.forEach(el => {
            if (_this.exitCond(el)) {
              return
            }
            if (el.dataset.option !== 'window') return
            if (usesHybridUnits(selection)) {
              el.classList.remove('d-hide')
            } else {
              el.classList.add('d-hide')
              if (this.settings.bin === 'window') {
                this.settings.bin = 'day'
                this.setActiveOptionBtn(this.settings.bin, this.binSizeTargets)
              }
            }
          })
          // if bin is blocks, hide option 'All' in zoom
          if (this.settings.bin === 'block') {
            // if zoom is all, change to year
            const selectedZoom = this.selectedZoom()
            if (!selectedZoom || selectedZoom === '' || selectedZoom === 'all') {
              this.settings.zoom = ''
              this.setActiveOptionBtn('year', this.zoomOptionTargets)
            }
            // hide 'All' option
            this.hideOptionBtn('all', this.zoomOptionTargets)
          } else {
            // else, show 'All' option in zoom
            this.showOptionBtn('all', this.zoomOptionTargets)
          }
        } else {
          this.settings.bin = 'day'
          this.setActiveOptionBtn(this.settings.bin, this.binSizeTargets)
          this.hideMultiTargets(this.binSelectorTargets)
          this.showOptionBtn('all', this.zoomOptionTargets)
        }
      }
      url += `?bin=${this.settings.bin}`

      this.settings.axis = this.selectedAxis()
      if (!this.settings.axis) this.settings.axis = 'time' // Set the default.
      url += `&axis=${this.settings.axis}`
      this.setActiveOptionBtn(this.settings.axis, this.axisOptionTargets)
      const chartResponse = await requestJSON(url)
      selectedChart = selection
      this.plotGraph(selection, chartResponse)
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
      case 'block-size':
        return `Displays the historical average size of ${this.getChainName()} blocks, reflecting how much transaction data each block contains.`
      case 'blockchain-size':
        return `Shows the total size of the ${this.getChainName()} blockchain, representing cumulative data stored over time.`
      case 'tx-count':
        return `Displays the number of transactions recorded on the ${this.getChainName()} network over time.`
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
      case 'pow-difficulty':
        return `${this.getChainName()} mining difficulty (Proof-of-Work): a measure of how hard it is to find a valid block over time.`
      case 'coin-supply':
        return `Shows the total circulating supply of ${this.getChainName()} coins over time.`
      case 'fees':
        return `Total transaction fees paid to ${this.getChainName()} miners over time.`
      case 'duration-btw-blocks':
        return `Shows the average time interval between consecutive ${this.getChainName()} blocks over time.`
      case 'hashrate':
        return `Total computational power securing the ${this.getChainName()} network over time.`
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

  defaultVisibilityFor (chart) {
    if (chart === 'coin-supply') return 'true-true-false'
    return null
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
    if (s.axis === this.defaultAxis) s.axis = null
    if (s.mode === this.defaultMode) s.mode = null
    if (s.visibility && s.visibility === this.defaultVisibilityFor(this.settings.chart)) s.visibility = null
    if (s.zoom && this.chartsView) {
      try {
        const ex = this.chartsView.xAxisExtremes()
        if (Zoom.mapKey(s.zoom, ex, this.isTimeAxis() ? 1 : avgBlockTime) === 'all') s.zoom = null
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
    this.setActiveOptionBtn(option, this.zoomOptionTargets)
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
        if (y1label) {
          const yAxis = axes.y
          if (yAxis) {
            const yLabelWidth = yAxis.axisLabelWidth
            if (yLabelWidth) {
              y1label.style.top = (Number(yLabelWidth) + 5) + 'px'
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

  setZoom (e) {
    const target = e.srcElement || e.target
    let option
    if (!target) {
      const ex = this.chartsView.xAxisExtremes()
      option = Zoom.mapKey(e, ex, this.isTimeAxis() ? 1 : avgBlockTime)
    } else {
      option = target.dataset.option
    }
    this.setActiveOptionBtn(option, this.zoomOptionTargets)
    if (!target) return // Exit if running for the first time
    this.validateZoom()
  }

  setBin (e) {
    const target = e.srcElement || e.target
    const option = target ? target.dataset.option : e
    if (!option) return
    this.setActiveOptionBtn(option, this.binSizeTargets)
    if (!target) return // Exit if running for the first time.
    selectedChart = null // Force fetch
    this.selectChart()
  }

  setScale (e) {
    const target = e.srcElement || e.target
    const option = target ? target.dataset.option : e
    if (!option) return
    this.setActiveOptionBtn(option, this.scaleTypeTargets)
    if (!target) return // Exit if running for the first time.
    if (this.chartsView) {
      this.chartsView.updateOptions({ logscale: option === 'log' })
    }
    this.settings.scale = option
    if (!this.isHomepage) {
      this.query.replace(this.filteredSettings())
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
      case 'coin-supply':
        if (this.visibility.length !== 3) {
          this.visibility = [true, true, false]
        }
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
    const _this = this
    optTargets.forEach(li => {
      if (_this.exitCond(li)) {
        return
      }
      if (li.dataset.option === opt) {
        li.classList.add('active')
      } else {
        li.classList.remove('active')
      }
    })
  }

  hideOptionBtn (opt, optTargets) {
    const _this = this
    optTargets.forEach(li => {
      if (_this.exitCond(li)) {
        return
      }
      if (li.dataset.option === opt) {
        li.classList.add('d-hide')
      }
    })
  }

  showOptionBtn (opt, optTargets) {
    const _this = this
    optTargets.forEach(li => {
      if (_this.exitCond(li)) {
        return
      }
      if (li.dataset.option === opt) {
        li.classList.remove('d-hide')
      }
    })
  }

  selectedZoom () { return this.selectedOption(this.zoomOptionTargets) }
  selectedBin () { return this.selectedOption(this.binSizeTargets) }
  selectedScale () { return this.selectedOption(this.scaleTypeTargets) }
  selectedAxis () { return this.selectedNormalOption(this.axisOptionTargets) }

  hideMultiTargets (opts) {
    opts.forEach((opt) => {
      opt.classList.add('d-hide')
    })
  }

  showMultiTargets (opts) {
    opts.forEach((opt) => {
      if ((isMobile() && !opt.classList.contains('mobile-mode')) || (!isMobile() && opt.classList.contains('mobile-mode'))) {
        opt.classList.add('d-hide')
      } else {
        opt.classList.remove('d-hide')
        opt.classList.remove('d-none')
      }
    })
  }

  selectedOption (optTargets) {
    let key = false
    const _this = this
    optTargets.forEach((el) => {
      if (_this.exitCond(el)) return
      if (el.classList.contains('active')) key = el.dataset.option
    })
    return key
  }

  selectedNormalOption (optTargets) {
    let key = false
    optTargets.forEach((el) => {
      if (el.classList.contains('active')) key = el.dataset.option
    })
    return key
  }

  exitCond (opt) {
    return (isMobile() && !opt.classList.contains('mobile-mode')) || (!isMobile() && opt.classList.contains('mobile-mode'))
  }

  getSelectedChart () {
    let selected = 'block-size'
    const _this = this
    this.chartSelectTargets.forEach((chart) => {
      if (_this.exitCond(chart)) {
        return
      }
      selected = chart.value
    })
    return selected
  }

  setSelectedChart (select) {
    const _this = this
    this.chartSelectTargets.forEach((chart) => {
      if (_this.exitCond(chart)) {
        return
      }
      chart.value = select
    })
  }

  getTargetsChecked (targets) {
    const _this = this
    let checked = false
    targets.forEach((target) => {
      if (_this.exitCond(target)) {
        return
      }
      checked = target.checked
    })
    return checked
  }

  setTargetsChecked (targets, checked) {
    const _this = this
    targets.forEach((target) => {
      if (_this.exitCond(target)) {
        return
      }
      target.checked = checked
    })
  }
}
