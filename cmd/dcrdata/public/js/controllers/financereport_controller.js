import { Controller } from '@hotwired/stimulus'
import TurboQuery from '../helpers/turbolinks_helper'
import { requestJSON } from '../helpers/http'
import humanize from '../helpers/humanize_helper'
import { isEmpty } from 'lodash-es'
import { getDefault } from '../helpers/module_helper'
import { padPoints, sizedBarPlotter } from '../helpers/chart_helper'
import Zoom from '../helpers/zoom_helper'

const responseCache = {}
let requestCounter = 0
let responseData
let proposalResponse = null
let treasuryResponse = null
let isSearching = false
let domainChartData = null
let domainChartYearData = null
let domainYearData = null
let combinedChartData = null
let combinedChartYearData = null
let combinedData = null
let combinedYearData = null
let combineBalanceMap = null
let combinedYearlyBalanceMap = null
let treasuryBalanceMap = null
let treasuryYearlyBalanceMap = null
let adminBalanceMap = null
let adminYearlyBalanceMap = null
const treasurySummaryData = null
let redrawChart = true
let domainChartZoom
let treasuryChartZoom
let domainChartBin
let treasuryChartBin
// let domainChartFlow
// let treasuryChartFlow

const proposalNote = '*The data is the daily cost estimate based on the total budget divided by the total number of proposals days.'
let treasuryNote = ''

function hasCache (k) {
  if (!responseCache[k]) return false
  const expiration = new Date(responseCache[k].expiration)
  return expiration > new Date()
}

// start function and variable for chart
const blockDuration = 5 * 60000
let Dygraph // lazy loaded on connect

function txTypesFunc (d, binSize) {
  const p = []

  d.time.map((n, i) => {
    p.push([new Date(n), d.sentRtx[i], d.receivedRtx[i], d.tickets[i], d.votes[i], d.revokeTx[i]])
  })

  padPoints(p, binSize)

  return p
}

function amountFlowProcessor (d, binSize) {
  const flowData = []
  const balanceData = []
  let balance = 0

  d.time.map((n, i) => {
    const v = d.net[i]
    let netReceived = 0
    let netSent = 0

    v > 0 ? (netReceived = v) : (netSent = (v * -1))
    flowData.push([new Date(n), d.received[i], d.sent[i], netReceived, netSent])
    balance += v
    balanceData.push([new Date(n), balance])
  })

  padPoints(flowData, binSize)
  padPoints(balanceData, binSize, true)

  return {
    flow: flowData,
    balance: balanceData
  }
}

function domainChartProcessor (d, binSize) {
  const flowData = []

  d.time.map((n, i) => {
    flowData.push([new Date(n), d.marketing[i], d.development[i]])
  })

  padPoints(flowData, binSize)

  return {
    flow: flowData
  }
}

function customizedFormatter (data) {
  let xHTML = ''
  if (data.xHTML !== undefined) {
    xHTML = humanize.date(data.x, false, true)
  }
  let html = this.getLabels()[0] + ': ' + xHTML
  data.series.map((series) => {
    if (series.color === undefined) return ''
    // Skip display of zeros
    if (series.y === 0) return ''
    const l = '<span style="color: ' + series.color + ';"> ' + series.labelHTML
    html = '<span style="color:#2d2d2d;">' + html + '</span>'
    html += '<br>' + series.dashHTML + l + ': ' + (isNaN(series.y) ? '' : series.y + ' DCR') + '</span> '
  })
  return html
}

function domainChartFormatter (data) {
  let xHTML = ''
  if (data.xHTML !== undefined) {
    xHTML = humanize.date(data.x, false, true)
  }
  let html = this.getLabels()[0] + ': ' + xHTML
  data.series.map((series) => {
    if (series.color === undefined) return ''
    // Skip display of zeros
    if (series.y === 0) return ''
    const l = '<span style="color: ' + series.color + ';"> ' + series.labelHTML
    html = '<span style="color:#2d2d2d;">' + html + '</span>'
    html += '<br>' + series.dashHTML + l + ': $' + (isNaN(series.y) ? '' : humanize.formatToLocalString(Number(series.y), 2, 2)) + '</span> '
  })
  return html
}

let commonOptions, amountFlowGraphOptions, balanceGraphOptions, domainGraphOptions
// Cannot set these until DyGraph is fetched.
function createOptions () {
  commonOptions = {
    digitsAfterDecimal: 8,
    showRangeSelector: true,
    rangeSelectorHeight: 20,
    rangeSelectorForegroundStrokeColor: '#999',
    rangeSelectorBackgroundStrokeColor: '#777',
    legend: 'follow',
    fillAlpha: 0.9,
    labelsKMB: true,
    labelsUTC: true,
    stepPlot: false,
    rangeSelectorPlotFillColor: 'rgba(128, 128, 128, 0.3)',
    rangeSelectorPlotFillGradientColor: 'transparent',
    rangeSelectorPlotStrokeColor: 'rgba(128, 128, 128, 0.7)',
    rangeSelectorPlotLineWidth: 2
  }

  amountFlowGraphOptions = {
    labels: ['Date', 'Income', 'Outgoing', 'Net Received', 'Net Spent'],
    colors: ['#2971FF', '#2ED6A1', '#41BF53', '#FF0090'],
    ylabel: 'Total (DCR)',
    visibility: [true, false, false, false],
    legendFormatter: customizedFormatter,
    stackedGraph: true,
    fillGraph: false
  }

  domainGraphOptions = {
    labels: ['Date', 'Marketing', 'Development'],
    colors: ['#2971FF', '#2ED6A1'],
    ylabel: 'Total Spend (USD)',
    visibility: [true, true],
    legendFormatter: domainChartFormatter,
    stackedGraph: true,
    fillGraph: false
  }

  balanceGraphOptions = {
    labels: ['Date', 'Balance'],
    colors: ['#41BF53'],
    ylabel: 'Balance (DCR)',
    plotter: [Dygraph.Plotters.linePlotter, Dygraph.Plotters.fillPlotter],
    legendFormatter: customizedFormatter,
    stackedGraph: false,
    visibility: [true],
    fillGraph: true,
    stepPlot: true
  }
}

let ctrl = null
// end function and varibable for chart

export default class extends Controller {
  static get targets () {
    return ['report', 'colorNoteRow', 'colorLabel', 'colorDescription',
      'interval', 'groupBy', 'searchInput', 'searchBtn', 'clearSearchBtn', 'searchBox', 'nodata',
      'treasuryToggleArea', 'reportDescription', 'reportAllPage',
      'activeProposalSwitchArea', 'options', 'flow', 'zoom', 'cinterval', 'chartbox', 'noconfirms',
      'chart', 'chartLoader', 'expando', 'littlechart', 'bigchart', 'fullscreen', 'treasuryChart', 'treasuryChartTitle',
      'yearSelect', 'ttype', 'yearSelectTitle', 'treasuryTypeTitle', 'groupByLabel', 'typeSelector',
      'amountFlowOption', 'balanceOption', 'chartHeader', 'outgoingExp', 'nameMatrixSwitch',
      'weekZoomBtn', 'dayZoomBtn', 'weekGroupBtn', 'dayGroupBtn', 'blockGroupBtn', 'sentRadioLabel', 'receivedRadioLabel',
      'netSelectRadio', 'selectTreasuryType', 'proposalSelectType', 'proposalType', 'listLabel', 'monthLabel',
      'currentBalanceArea', 'treasuryBalanceDisplay', 'treasuryLegacyPercent', 'treasuryTypeRate', 'chartData',
      'specialTreasury', 'decentralizedData', 'adminData', 'domainFutureRow', 'futureLabel', 'reportType', 'pageLoader']
  }

  async connect () {
    ctrl = this
    ctrl.state = Object.assign({}, ctrl.settings)
    // Get initial view settings from the url
    ctrl.setChartType()
    if (ctrl.settings.flow) {
      ctrl.setFlowChecks()
    } else {
      ctrl.settings.flow = ctrl.defaultSettings.flow
      // if (this.isDomainType()) {
      //   domainChartFlow = ctrl.settings.flow
      // } else if (this.settings.type === 'treasury') {
      //   treasuryChartFlow = ctrl.settings.flow
      // }
      ctrl.setFlowChecks()
    }
    if (ctrl.settings.zoom !== null) {
      ctrl.zoomButtons.forEach((button) => {
        button.classList.remove('btn-selected')
      })
    }
    if (ctrl.settings.bin == null) {
      ctrl.settings.bin = ctrl.getBin()
      if (this.isDomainType()) {
        domainChartBin = ctrl.settings.bin
      } else if (this.settings.type === 'treasury') {
        treasuryChartBin = ctrl.settings.bin
      }
    }
    if (ctrl.settings.chart == null || !ctrl.validChartType(ctrl.settings.chart)) {
      ctrl.settings.chart = ctrl.chartType
    }

    Dygraph = await getDefault(
      import('../vendor/dygraphs.min.js')
    )
    ctrl.currentTType = ctrl.settings.ttype
    ctrl.changedTType = null
    this.calculate()
  }

  _drawCallback (graph, first) {
    if (first) return
    const [start, end] = ctrl.graph.xAxisRange()
    if (start === end) return
    if (end === this.lastEnd) return // Only handle slide event.
    this.lastEnd = end
    ctrl.settings.zoom = Zoom.encode(start, end)
    // console.log('zoom callback: ' + ctrl.settings.zoom)
    // if (this.isTreasuryReport()) {
    //   treasuryChartZoom = ctrl.settings.zoom
    // } else if (this.isDomainType()) {
    //   domainChartZoom = ctrl.settings.zoom
    // }
    ctrl.updateQueryString()
    ctrl.setSelectedZoom(Zoom.mapKey(ctrl.settings.zoom, ctrl.graph.xAxisExtremes()))
  }

  _zoomCallback (start, end) {
    ctrl.zoomButtons.forEach((button) => {
      button.classList.remove('btn-selected')
    })
    ctrl.settings.zoom = Zoom.encode(start, end)
    // console.log('zoom callback: ' + ctrl.settings.zoom)
    // if (this.isTreasuryReport()) {
    //   treasuryChartZoom = ctrl.settings.zoom
    // } else if (this.isDomainType()) {
    //   domainChartZoom = ctrl.settings.zoom
    // }
    ctrl.updateQueryString()
    ctrl.setSelectedZoom(Zoom.mapKey(ctrl.settings.zoom, ctrl.graph.xAxisExtremes()))
  }

  setSelectedZoom (zoomKey) {
    this.zoomButtons.forEach(function (button) {
      if (button.name === zoomKey) {
        button.classList.add('btn-selected')
      } else {
        button.classList.remove('btn-selected')
      }
    })
  }

  drawGraph () {
    const settings = ctrl.settings
    ctrl.noconfirmsTarget.classList.add('d-hide')
    ctrl.chartTarget.classList.remove('d-hide')
    // Check for invalid view parameters
    if (!ctrl.validChartType(settings.chart) || !ctrl.validGraphInterval()) return
    if (settings.chart === ctrl.state.chart && settings.bin === ctrl.state.bin) {
      // Only the zoom has changed.
      const zoom = Zoom.decode(settings.zoom)
      if (zoom) {
        ctrl.setZoom(zoom.start, zoom.end)
      }
      return
    }
    // Set the current view to prevent unnecessary reloads.
    Object.assign(ctrl.state, settings)
    ctrl.fetchGraphData(settings.chart, settings.bin)
  }

  setIntervalButton (interval) {
    const button = ctrl.validGraphInterval(interval)
    if (!button) return false
    ctrl.binputs.forEach((button) => {
      button.classList.remove('btn-selected')
    })
    button.classList.add('btn-selected')
  }

  validGraphInterval (interval) {
    const bin = interval || this.settings.bin || this.activeBin
    let b = false
    this.binputs.forEach((button) => {
      if (button.name === bin) b = button
    })
    return b
  }

  getBin () {
    let bin = ctrl.query.get('bin')
    if (!ctrl.setIntervalButton(bin)) {
      bin = ctrl.activeBin
    }
    return bin
  }

  setFlowChecks () {
    const bitmap = this.settings.flow
    this.flowBoxes.forEach((box) => {
      box.checked = bitmap & parseInt(box.value)
    })
  }

  disconnect () {
    if (this.graph !== undefined) {
      this.graph.destroy()
    }
    this.retrievedData = {}
  }

  validChartType (chart) {
    return this.optionsTarget.namedItem(chart) || false
  }

  setChartType () {
    const chart = this.settings.chart
    if (this.validChartType(chart)) {
      this.optionsTarget.value = chart
    }
  }

  // Request the initial chart data, grabbing the Dygraph script if necessary.
  initializeChart () {
    createOptions()
    // If no chart data has been requested, e.g. when initially on the
    // list tab, then fetch the initial chart data.
    this.fetchGraphData(this.chartType, this.getBin())
  }

  async fetchGraphData (chart, bin) {
    const cacheKey = chart + '-' + bin
    if (ctrl.ajaxing === cacheKey) {
      return
    }
    ctrl.requestedChart = cacheKey
    ctrl.ajaxing = cacheKey

    ctrl.chartLoaderTarget.classList.add('loading')
    // if not change type
    if (this.isDomainType() || (!ctrl.currentTType && ctrl.currentTType === ctrl.changedTType)) {
      // Check for cached data
      if (ctrl.retrievedData[cacheKey]) {
        // Queue the function to allow the loader to display.
        setTimeout(() => {
          ctrl.popChartCache(chart, bin)
          ctrl.chartLoaderTarget.classList.remove('loading')
          ctrl.ajaxing = false
        }, 10) // 0 should work but doesn't always
        return
      }
    }
    // if else, get data from api
    let graphDataResponse = null
    if (this.settings.type === 'treasury' && (bin === 'week' || bin === 'day') && this.settings.ttype !== 'current' && this.settings.ttype !== 'legacy') {
      const treasuryUrl = `/api/treasury/io/${bin}`
      const legacyUrl = '/api/address/' + ctrl.devAddress + '/amountflow/' + bin
      const treasuryRes = await requestJSON(treasuryUrl)
      const legacyRes = await requestJSON(legacyUrl)
      graphDataResponse = this.combinedDataHandler(treasuryRes, legacyRes)
    } else {
      let url = `/api/treasury/io/${bin}`
      if (!this.isDomainType() && this.settings.ttype === 'legacy') {
        url = '/api/address/' + ctrl.devAddress + '/amountflow/' + bin
      }
      graphDataResponse = this.isDomainType() ? (bin === 'year' ? domainChartYearData : domainChartData) : (this.settings.ttype !== 'current' && this.settings.ttype !== 'legacy') ? (bin === 'year' ? combinedChartYearData : combinedChartData) : await requestJSON(url)
    }
    ctrl.processData(chart, bin, graphDataResponse)
    this.currentTType = this.changedTType
    ctrl.ajaxing = false
    ctrl.chartLoaderTarget.classList.remove('loading')
  }

  isDomainType () {
    return (this.settings.type === '' || this.settings.type === 'proposal' || this.settings.type === 'summary') && this.settings.pgroup === 'domains'
  }

  combinedDataHandler (treasuryRes, legacyRes) {
    // create time map
    const timeArr = []
    // return combined data
    const combinedDataMap = new Map()
    const _this = this
    if (treasuryRes && treasuryRes.time) {
      treasuryRes.time.map((time, index) => {
        const dateTime = new Date(time)
        const dateTimeStr = _this.getStringFromDate(dateTime)
        timeArr.push(dateTimeStr)
        const item = {
          time: dateTimeStr,
          received: treasuryRes.received[index],
          sent: treasuryRes.sent[index],
          net: treasuryRes.net[index]
        }
        combinedDataMap.set(dateTimeStr, item)
      })
    }

    if (legacyRes && legacyRes.time) {
      legacyRes.time.map((time, index) => {
        const dateTime = new Date(time)
        const dateTimeStr = _this.getStringFromDate(dateTime)
        if (!timeArr.includes(dateTimeStr)) {
          timeArr.push(dateTimeStr)
          const item = {
            time: dateTimeStr,
            received: legacyRes.received[index],
            sent: legacyRes.sent[index],
            net: legacyRes.net[index]
          }
          combinedDataMap.set(dateTimeStr, item)
        } else {
          const item = combinedDataMap.get(dateTimeStr)
          item.received = Number(item.received) + Number(legacyRes.received[index])
          item.sent = Number(item.sent) + Number(legacyRes.sent[index])
          item.net = Number(item.net) + Number(legacyRes.net[index])
          combinedDataMap.set(dateTimeStr, item)
        }
      })
    }

    timeArr.sort(function (a, b) {
      const aTime = new Date(a)
      const bTime = new Date(b)
      if (aTime > bTime) {
        return 1
      }
      if (aTime < bTime) {
        return -1
      }
      return 0
    })

    const timeInsertArr = []
    const receivedArr = []
    const sentArr = []
    const netArr = []
    timeArr.forEach((time) => {
      timeInsertArr.push(time + 'T07:00:00Z')
      const item = combinedDataMap.get(time)
      receivedArr.push(item.received)
      sentArr.push(item.sent)
      netArr.push(item.net)
    })
    const mainResult = {
      time: timeInsertArr,
      received: receivedArr,
      sent: sentArr,
      net: netArr
    }
    return mainResult
  }

  getStringFromDate (date) {
    let result = date.getFullYear() + '-'
    const month = date.getMonth() + 1
    result += (month < 10 ? '0' + month : month) + '-'
    const day = date.getDate()
    result += day < 10 ? '0' + day : day
    return result
  }

  processData (chart, bin, data) {
    if (isEmpty(data)) {
      ctrl.noDataAvailable()
      return
    }
    const binSize = Zoom.mapValue(bin) || blockDuration
    if (chart === 'types') {
      ctrl.retrievedData['types-' + bin] = txTypesFunc(data, binSize)
    } else if (chart === 'amountflow' || chart === 'balance') {
      const processed = this.isDomainType() ? domainChartProcessor(data, binSize) : amountFlowProcessor(data, binSize)
      ctrl.retrievedData['amountflow-' + bin] = processed.flow
      if (!ctrl.isDomainType()) {
        ctrl.retrievedData['balance-' + bin] = processed.balance
      }
    } else return
    setTimeout(() => {
      ctrl.popChartCache(chart, bin)
    }, 0)
  }

  noDataAvailable () {
    this.noconfirmsTarget.classList.remove('d-hide')
    this.chartTarget.classList.add('d-hide')
    this.chartLoaderTarget.classList.remove('loading')
  }

  popChartCache (chart, bin) {
    const cacheKey = chart + '-' + bin
    const binSize = Zoom.mapValue(bin) || blockDuration
    if (!ctrl.retrievedData[cacheKey] ||
      ctrl.requestedChart !== cacheKey
    ) {
      return
    }
    const data = ctrl.retrievedData[cacheKey]
    let options = null
    ctrl.flowTarget.classList.add('d-hide')
    switch (chart) {
      case 'amountflow':
        options = this.isDomainType() ? domainGraphOptions : amountFlowGraphOptions
        options.plotter = sizedBarPlotter(binSize)
        ctrl.flowTarget.classList.remove('d-hide')
        break
      case 'balance':
        options = balanceGraphOptions
        break
    }
    options.zoomCallback = null
    options.drawCallback = null
    if (ctrl.graph === undefined) {
      ctrl.graph = ctrl.createGraph(data, options)
    } else {
      ctrl.graph.updateOptions({
        ...{ file: data },
        ...options
      })
    }
    if (chart === 'amountflow') {
      ctrl.updateFlow()
    }
    ctrl.chartLoaderTarget.classList.remove('loading')
    ctrl.xRange = ctrl.graph.xAxisExtremes()
    ctrl.validateZoom(binSize)
  }

  changeBin (e) {
    const target = e.srcElement || e.target
    if (target.nodeName !== 'BUTTON') return
    ctrl.settings.bin = target.name
    // TODO
    if (this.isTreasuryReport()) {
      treasuryChartBin = ctrl.settings.bin
      console.log('set treasury chart bin: ' + treasuryChartBin)
    } else if (this.isDomainType()) {
      domainChartBin = ctrl.settings.bin
      console.log('set domain chart bin: ' + domainChartBin)
    }
    ctrl.setIntervalButton(target.name)
    this.handlerBinSelectorWhenChange()
    if (target.name === 'year') {
      if (ctrl.zoomButtons) {
        ctrl.zoomButtons.forEach((button) => {
          if (button.name === 'all') {
            button.click()
          }
        })
      }
    }
    this.updateQueryString()
    this.drawGraph()
  }

  changeGraph (e) {
    this.settings.chart = this.chartType
    if (this.settings.type === 'treasury') {
      this.treasuryChart = this.settings.chart
    }
    this.updateQueryString()
    this.drawGraph()
  }

  validateZoom (binSize) {
    ctrl.setButtonVisibility()
    console.log('settings zoom: ' + ctrl.settings.zoom)
    console.log('ctrl xRange: ' + ctrl.xRange)
    const zoom = Zoom.validate(ctrl.activeZoomKey || ctrl.settings.zoom, ctrl.xRange, binSize)
    ctrl.setZoom(zoom.start, zoom.end)
    ctrl.graph.updateOptions({
      zoomCallback: ctrl.zoomCallback,
      drawCallback: ctrl.drawCallback
    })
  }

  setZoom (start, end) {
    ctrl.chartLoaderTarget.classList.add('loading')
    ctrl.graph.updateOptions({
      dateWindow: [start, end]
    })
    ctrl.settings.zoom = Zoom.encode(start, end)
    console.log('check settings zoom on setFunction: ' + ctrl.settings.zoom)
    if (this.isTreasuryReport()) {
      treasuryChartZoom = ctrl.settings.zoom
      console.log('set treasury chart zoom: ' + treasuryChartZoom)
    } else if (this.isDomainType()) {
      domainChartZoom = ctrl.settings.zoom
      console.log('set domain chart zoom: ' + domainChartZoom)
    }
    ctrl.lastEnd = end
    ctrl.updateQueryString()
    ctrl.chartLoaderTarget.classList.remove('loading')
  }

  onZoom (e) {
    const target = e.srcElement || e.target
    if (target.nodeName !== 'BUTTON') return
    ctrl.zoomButtons.forEach((button) => {
      button.classList.remove('btn-selected')
    })
    target.classList.add('btn-selected')
    if (ctrl.graph === undefined) {
      return
    }

    this.handlerAllowChartSelector()
    const duration = ctrl.activeZoomDuration
    const end = ctrl.xRange[1]
    const start = duration === 0 ? ctrl.xRange[0] : end - duration
    ctrl.setZoom(start, end)
  }

  handlerBinSelectorWhenChange () {
    switch (this.activeBin) {
      case 'all':
        this.handlerZoomDisabledBySelector(['all'])
        break
      default:
        this.handlerZoomDisabledBySelector([])
    }
  }

  handlerZoomDisabledBySelector (disabledTypes) {
    this.zoomButtons.forEach((button) => {
      if (disabledTypes.includes(button.name)) {
        button.disabled = true
      } else {
        button.disabled = false
      }
    })
  }

  handlerAllowChartSelector () {
    const activeButtons = this.zoomTarget.getElementsByClassName('btn-selected')
    if (activeButtons.length === 0) return
    const activeZoomType = activeButtons[0].name
    switch (activeZoomType) {
      case 'all':
        if (this.activeBin === 'all') {
          ctrl.binputs.forEach((button) => {
            if (button.name === 'year') {
              button.click()
            }
          })
        }
        this.handlerGroupDisabledBySelector(['all'])
        break
      case 'year':
        this.handlerGroupDisabledBySelector([])
        break
      case 'month':
        // if current group type is year, change to month
        if (this.activeBin === 'year') {
          ctrl.binputs.forEach((button) => {
            if (button.name === 'month') {
              button.click()
            }
          })
        }
        this.handlerGroupDisabledBySelector(['year'])
        break
      case 'week':
        if (this.activeBin === 'year' || this.activeBin === 'month') {
          ctrl.binputs.forEach((button) => {
            if (button.name === 'week') {
              button.click()
            }
          })
        }
        this.handlerGroupDisabledBySelector(['year', 'month'])
        break
      case 'day':
        if (this.activeBin === 'year' || this.activeBin === 'month' || this.activeBin === 'week') {
          ctrl.binputs.forEach((button) => {
            if (button.name === 'day') {
              button.click()
            }
          })
        }
        this.handlerGroupDisabledBySelector(['year', 'month', 'week'])
        break
    }
  }

  handlerGroupDisabledBySelector (disabledTypes) {
    this.binputs.forEach((button) => {
      if (disabledTypes.includes(button.name)) {
        button.disabled = true
      } else {
        button.disabled = false
      }
    })
  }

  toggleExpand (e) {
    const btn = this.expandoTarget
    if (btn.classList.contains('dcricon-expand')) {
      btn.classList.remove('dcricon-expand')
      btn.classList.add('dcricon-collapse')
      this.bigchartTarget.appendChild(this.chartboxTarget)
      this.fullscreenTarget.classList.remove('d-none')
    } else {
      this.putChartBack()
    }
    if (this.graph) this.graph.resize()
  }

  putChartBack () {
    const btn = this.expandoTarget
    btn.classList.add('dcricon-expand')
    btn.classList.remove('dcricon-collapse')
    this.littlechartTarget.appendChild(this.chartboxTarget)
    this.fullscreenTarget.classList.add('d-none')
    if (this.graph) this.graph.resize()
  }

  exitFullscreen (e) {
    if (e.target !== this.fullscreenTarget) return
    this.putChartBack()
  }

  setButtonVisibility () {
    const duration = ctrl.chartDuration
    const buttonSets = [ctrl.zoomButtons, ctrl.binputs]
    buttonSets.forEach((buttonSet) => {
      buttonSet.forEach((button) => {
        if (button.dataset.fixed) return
        if (duration > Zoom.mapValue(button.name)) {
          button.classList.remove('d-hide')
        } else {
          button.classList.remove('btn-selected')
          button.classList.add('d-hide')
        }
      })
    })
  }

  updateFlow () {
    const bitmap = ctrl.flow
    if (bitmap === 0) {
      // If all boxes are unchecked, just leave the last view
      // in place to prevent chart errors with zero visible datasets
      return
    }
    ctrl.settings.flow = bitmap
    // if (this.isDomainType()) {
    //   domainChartFlow = ctrl.settings.flow
    // } else if (this.settings.type === 'treasury') {
    //   treasuryChartFlow = ctrl.settings.flow
    // }
    ctrl.updateQueryString()
    // Set the graph dataset visibility based on the bitmap
    // Dygraph dataset indices: 0 received, 1 sent, 2 & 3 net
    const visibility = {}
    visibility[0] = bitmap & 1
    visibility[1] = bitmap & 2
    visibility[2] = visibility[3] = bitmap & 4
    Object.keys(visibility).forEach((idx) => {
      ctrl.graph.setVisibility(idx, visibility[idx])
    })
  }

  createGraph (processedData, otherOptions) {
    return new Dygraph(
      this.chartTarget,
      processedData,
      { ...commonOptions, ...otherOptions }
    )
  }

  bindElements () {
    this.flowBoxes = this.flowTarget.querySelectorAll('input')
    this.zoomButtons = this.zoomTarget.querySelectorAll('button')
    this.binputs = this.cintervalTarget.querySelectorAll('button')
  }

  async initialize () {
    this.query = new TurboQuery()
    this.settings = TurboQuery.nullTemplate([
      'chart', 'zoom', 'bin', 'flow', 'type', 'tsort', 'psort', 'stype',
      'order', 'interval', 'search', 'usd', 'active', 'year', 'ttype', 'pgroup', 'ptype'
    ])

    this.defaultSettings = {
      type: 'proposal',
      tsort: 'newest',
      psort: 'newest',
      pgroup: 'proposals',
      stype: 'startdt',
      order: 'desc',
      interval: 'month',
      ptype: 'month',
      search: '',
      usd: false,
      active: true,
      chart: 'amountflow',
      zoom: '',
      bin: 'month',
      flow: 3,
      year: 0, // 0 when display all year
      ttype: 'combined'
    }
    this.query.update(this.settings)
    this.treasuryChart = 'balance'
    this.proposalTSort = 'oldest'
    this.treasuryTSort = 'newest'
    this.isMonthDisplay = false
    this.devAddress = this.data.get('devAddress')
    treasuryNote = `*All numbers are pulled from the blockchain. Includes <a href="/treasury">treasury</a> and <a href="/address/${this.devAddress}">legacy</a> data.`
    treasuryChartZoom = undefined
    domainChartZoom = undefined
    domainChartBin = undefined
    // domainChartFlow = undefined
    treasuryChartBin = undefined
    // treasuryChartFlow = undefined
    this.initData()
    this.isMonthDisplay = this.isProposalMonthReport() || this.isAuthorMonthGroup()
    this.retrievedData = {}
    this.ajaxing = false
    this.requestedChart = false
    // Bind functions that are passed as callbacks
    this.zoomCallback = this._zoomCallback.bind(this)
    this.drawCallback = this._drawCallback.bind(this)
    this.lastEnd = 0
    this.bindElements()
    // These two are templates for query parameter sets.
    // When url query parameters are set, these will also be updated.
    // Parse stimulus data
    const cdata = this.data
    this.dcrAddress = cdata.get('dcraddress')
    this.balance = cdata.get('balance')
  }

  async initData () {
    if (!this.settings.type) {
      this.settings.type = this.defaultSettings.type
    }
    if ((!this.settings.type || this.settings.type === 'proposal' || this.settings.type === 'summary') && !this.isDomainType()) {
      this.defaultSettings.tsort = 'oldest'
    } else {
      this.defaultSettings.tsort = 'newest'
    }
    if ((typeof this.settings.active) !== 'boolean') {
      this.settings.active = this.defaultSettings.active
    }
    if (!this.settings.ptype) {
      this.settings.ptype = this.defaultSettings.ptype
    }
    if (!this.settings.pgroup) {
      this.settings.pgroup = this.defaultSettings.pgroup
    }
    if (this.settings.type && this.settings.type === 'treasury') {
      this.defaultSettings.stype = ''
    }

    if (this.settings.type === 'treasury' || this.isDomainType()) {
      this.settings.tsort = this.treasuryTSort
      // if (this.isDomainType()) {
      //   this.settings.zoom = domainChartZoom || this.settings.zoom
      //   this.settings.bin = domainChartBin || this.settings.bin
      //   this.settings.flow = domainChartFlow || this.settings.flow
      //   console.log('set settings domain chart: ' + domainChartZoom)
      // } else if (this.settings.type === 'treasury') {
      //   this.settings.zoom = treasuryChartZoom || this.settings.zoom
      //   this.settings.bin = treasuryChartBin || this.settings.bin
      //   this.settings.flow = treasuryChartFlow || this.settings.flow
      //   console.log('set settings treasury chart: ' + treasuryChartZoom)
      // }
    } else {
      this.settings.tsort = this.proposalTSort
      this.settings.zoom = ''
      this.settings.bin = this.defaultSettings.bin
      this.settings.flow = this.defaultSettings.flow
    }

    if (!this.settings.interval) {
      this.settings.interval = this.defaultSettings.interval
    }
    if (!this.settings.ttype) {
      this.settings.ttype = this.defaultSettings.ttype
    }
    if (this.settings.search) {
      this.searchInputTarget.value = this.settings.search
      isSearching = true
      this.searchBtnTarget.classList.add('d-none')
      this.clearSearchBtnTarget.classList.remove('d-none')
    } else {
      this.searchBtnTarget.classList.remove('d-none')
      this.clearSearchBtnTarget.classList.add('d-none')
    }
    this.reportTypeTargets.forEach((rTypeTarget) => {
      rTypeTarget.classList.remove('active')
      if ((!this.settings.type && rTypeTarget.name === 'proposal') ||
      (rTypeTarget.name === 'proposal' && (this.settings.type === '' || this.settings.type === 'proposal' || this.settings.type === 'summary')) ||
      (rTypeTarget.name === 'treasury' && this.settings.type === 'treasury')) {
        rTypeTarget.classList.add('active')
      }
    })

    if (this.settings.type !== 'proposal' && this.settings.type !== '' && this.settings.type !== 'summary') {
      this.intervalTargets.forEach((intervalTarget) => {
        intervalTarget.classList.remove('active')
        if (intervalTarget.name === this.settings.interval) {
          intervalTarget.classList.add('active')
        }
      })
      this.ttypeTargets.forEach((ttypeTarget) => {
        ttypeTarget.classList.remove('active')
        if ((ttypeTarget.name === this.settings.ttype) || (ttypeTarget.name === 'current' && !this.settings.ttype)) {
          ttypeTarget.classList.add('active')
        }
      })
    }

    if (this.settings.type === '' || this.settings.type === 'proposal' || this.settings.type === 'summary') {
      const $scroller = document.getElementById('scroller')
      const $container = document.getElementById('containerBody')
      const $wrapper = document.getElementById('wrapperReportTable')
      let ignoreScrollEvent = false
      let animation = null
      this.proposalTypeTargets.forEach((proposalTypeTarget) => {
        proposalTypeTarget.classList.remove('active')
        if ((proposalTypeTarget.name === this.settings.pgroup) || (proposalTypeTarget.name === 'proposals' && !this.settings.pgroup)) {
          proposalTypeTarget.classList.add('active')
        }
      })
      const scrollbarPositioner = () => {
        const scrollTop = document.scrollingElement.scrollTop
        const wrapperTop = $wrapper.offsetTop
        const wrapperBottom = wrapperTop + $wrapper.offsetHeight

        const topMatch = (window.innerHeight + scrollTop) >= wrapperTop
        const bottomMatch = (scrollTop) <= wrapperBottom

        if (topMatch && bottomMatch) {
          const inside = wrapperBottom >= scrollTop && window.innerHeight + scrollTop <= wrapperBottom

          if (inside) {
            $scroller.style.bottom = '0px'
          } else {
            const offset = (scrollTop + window.innerHeight) - wrapperBottom

            $scroller.style.bottom = offset + 'px'
          }
          $scroller.classList.add('visible')
        } else {
          $scroller.classList.remove('visible')
        }

        window.requestAnimationFrame(scrollbarPositioner)
      }

      window.requestAnimationFrame(scrollbarPositioner)

      $scroller.addEventListener('scroll', (e) => {
        if (ignoreScrollEvent) return false

        if (animation) window.cancelAnimationFrame(animation)
        animation = window.requestAnimationFrame(() => {
          ignoreScrollEvent = true
          $container.scrollLeft = $scroller.scrollLeft
          ignoreScrollEvent = false
        })
      })

      $container.addEventListener('scroll', (e) => {
        if (ignoreScrollEvent) return false

        if (animation) window.cancelAnimationFrame(animation)
        animation = window.requestAnimationFrame(() => {
          ignoreScrollEvent = true
          $scroller.scrollLeft = $container.scrollLeft

          ignoreScrollEvent = false
        })
      })
    }

    $(window).on('resize', function () {
      // get table thead size
      const tableWidthStr = $('#reportTable thead').css('width').replace('px', '')
      const tableWidth = parseFloat(tableWidthStr.trim())
      const parentContainerWidthStr = $('#repotParentContainer').css('width').replace('px', '')
      const parentContainerWidth = parseFloat(parentContainerWidthStr.trim())
      if (tableWidth < parentContainerWidth + 5) {
        $('#scroller').addClass('d-none')
      } else {
        $('#scroller').removeClass('d-none')
      }
      // set overflow class
      $('#scroller').css('width', $('#repotParentContainer').css('width'))
    })
  }

  searchInputKeypress (e) {
    if (e.keyCode === 13) {
      this.searchProposal()
    }
  }

  onTypeChange (e) {
    if (!e.target.value || e.target.value === '') {
      return
    }
    this.searchBtnTarget.classList.remove('d-none')
    this.clearSearchBtnTarget.classList.add('d-none')
  }

  searchProposal () {
    // if search key is empty, ignore
    if (!this.searchInputTarget.value || this.searchInputTarget.value === '') {
      this.searchBtnTarget.classList.remove('d-none')
      this.clearSearchBtnTarget.classList.add('d-none')
      if (isSearching) {
        this.settings.search = this.defaultSettings.search
        isSearching = false
        this.calculate()
      }
      return
    }
    this.searchBtnTarget.classList.add('d-none')
    this.clearSearchBtnTarget.classList.remove('d-none')
    this.settings.search = this.searchInputTarget.value
    this.calculate()
  }

  clearSearch (e) {
    this.settings.search = this.defaultSettings.search
    this.searchInputTarget.value = ''
    this.searchBtnTarget.classList.remove('d-none')
    this.clearSearchBtnTarget.classList.add('d-none')
    isSearching = false
    this.calculate()
  }

  updateQueryString () {
    const [query, settings, defaults] = [{}, this.settings, this.defaultSettings]
    for (const k in settings) {
      if ((((typeof settings[k]) !== 'boolean') && !settings[k]) || settings[k].toString() === defaults[k].toString()) continue
      query[k] = settings[k]
    }
    this.query.replace(query)
  }

  setReportTitle () {
    switch (this.settings.type) {
      case '':
      case 'proposal':
        this.reportDescriptionTarget.innerHTML = proposalNote
        if (this.settings.pgroup === '' || this.settings.pgroup === 'proposals') {
          this.settings.interval = this.defaultSettings.interval
          this.intervalTargets.forEach((intervalTarget) => {
            intervalTarget.classList.remove('active')
            if (intervalTarget.name === this.settings.interval) {
              intervalTarget.classList.add('active')
            }
          })
          this.reportDescriptionTarget.classList.add('d-none')
        } else if (this.settings.pgroup === 'authors') {
          if (this.settings.ptype !== 'list') {
            this.reportDescriptionTarget.classList.add('d-none')
          } else {
            this.reportDescriptionTarget.classList.remove('d-none')
            this.reportDescriptionTarget.innerHTML = proposalNote
          }
        }
        break
      case 'summary':
        this.reportDescriptionTarget.innerHTML = proposalNote
        break
      case 'domain':
        this.reportDescriptionTarget.innerHTML = proposalNote
        break
      case 'treasury':
        this.reportDescriptionTarget.innerHTML = treasuryNote
        break
      case 'author':
        this.reportDescriptionTarget.innerHTML = proposalNote
    }
  }

  treasuryTypeChange (e) {
    if (e.target.name === this.settings.ttype) {
      return
    }
    const target = e.srcElement || e.target
    this.ttypeTargets.forEach((ttypeTarget) => {
      ttypeTarget.classList.remove('active')
    })
    target.classList.add('active')
    this.settings.ttype = e.target.name
    this.changedTType = e.target.name
    redrawChart = true
    this.calculate()
  }

  async reportTypeChange (e) {
    if (((this.settings.type === '' || this.settings.type === 'proposal' || this.settings.type === 'summary') && e.target.name === 'proposal') ||
      (this.settings.type === 'treasury' && e.target.name === 'treasury')) {
      return
    }
    this.settings.type = e.target.name
    if (this.settings.type === 'treasury') {
      this.settings.chart = this.treasuryChart
      this.optionsTarget.value = this.settings.chart
    } else if ((this.settings.type === '' || this.settings.type === 'proposal') && !this.isMonthDisplay) {
      this.settings.type = 'summary'
    }
    redrawChart = true
    await this.initData()
    await this.connect()
  }

  async proposalTypeChange (e) {
    if (e.target.name === this.settings.pgroup) {
      return
    }
    const target = e.srcElement || e.target
    this.proposalTypeTargets.forEach((proposalTypeTarget) => {
      proposalTypeTarget.classList.remove('active')
    })
    target.classList.add('active')
    this.settings.pgroup = e.target.name
    if (this.settings.pgroup === '' || this.settings.pgroup === 'proposals') {
      this.settings.type = this.isMonthDisplay ? 'proposal' : 'summary'
      this.settings.ptype = this.defaultSettings.ptype
    } else if (this.settings.pgroup === 'authors') {
      this.settings.ptype = this.isMonthDisplay ? '' : 'list'
      this.settings.type = this.defaultSettings.type
    } else if (this.settings.pgroup === 'domains') {
      this.settings.ptype = 'list'
    }
    this.settings.stype = this.defaultSettings.stype
    await this.initData()
    await this.connect()
  }

  isProposalGroup () {
    return (this.settings.type === '' || this.settings.type === 'proposal' || this.settings.type === 'summary') && (this.settings.pgroup === '' || this.settings.pgroup === 'proposals')
  }

  isSummaryReport () {
    return this.settings.type === 'summary' && (this.settings.pgroup === '' || this.settings.pgroup === 'proposals')
  }

  isProposalMonthReport () {
    return (this.settings.type === '' || this.settings.type === 'proposal') && (this.settings.pgroup === '' || this.settings.pgroup === 'proposals')
  }

  isAuthorGroup () {
    return (this.settings.type === '' || this.settings.type === 'proposal' || this.settings.type === 'summary') && this.settings.pgroup === 'authors'
  }

  isAuthorListGroup () {
    return this.isAuthorGroup() && this.settings.ptype === 'list'
  }

  isAuthorMonthGroup () {
    return this.isAuthorGroup() && this.settings.ptype !== 'list'
  }

  isTreasuryReport () {
    return this.settings.type === 'treasury'
  }

  intervalChange (e) {
    if (e.target.name === ctrl.settings.interval) {
      return
    }
    const target = e.srcElement || e.target
    this.intervalTargets.forEach((intervalTarget) => {
      intervalTarget.classList.remove('active')
    })
    target.classList.add('active')
    ctrl.settings.interval = e.target.name
    // if (e.target.name === 'year') {
    //   if (ctrl.settings.bin !== 'year') {
    //     ctrl.binputs.forEach((button) => {
    //       if (button.name === 'year') {
    //         button.click()
    //       }
    //     })
    //     ctrl.zoomButtons.forEach((button) => {
    //       if (button.name === 'all') {
    //         button.click()
    //       }
    //     })
    //   }
    // } else {
    //   if (ctrl.settings.bin === 'year') {
    //     ctrl.binputs.forEach((button) => {
    //       if (button.name === 'month') {
    //         button.click()
    //       }
    //     })
    //     ctrl.zoomButtons.forEach((button) => {
    //       if (button.name === 'year') {
    //         button.click()
    //       }
    //     })
    //   }
    // }
    this.calculate()
  }

  getApiUrlByType () {
    switch (this.settings.type) {
      case 'treasury':
        return '/api/finance-report/treasury'
      default:
        return `/api/finance-report/proposal?search=${!this.settings.search ? '' : this.settings.search}`
    }
  }

  createReportTable () {
    if (this.settings.type === '' || this.settings.type === 'proposal' || this.settings.type === 'summary') {
      $('#reportTable').css('width', '')
    }
    // if summary, display toggle for filter Proposals are active
    if (this.settings.type === 'summary') {
      if (!this.settings.active) {
        document.getElementById('activeProposalInput').checked = false
      } else {
        document.getElementById('activeProposalInput').checked = true
      }
    }
    this.updateQueryString()

    if (this.settings.type === 'treasury') {
      this.treasuryToggleAreaTarget.classList.remove('d-none')
      this.createTreasuryTable(responseData)
      this.treasuryChartTarget.classList.remove('d-none')
      if (redrawChart) {
        redrawChart = false
        this.initializeChart()
        this.drawGraph()
        if (ctrl.zoomButtons) {
          ctrl.zoomButtons.forEach((button) => {
            if (button.name === 'year') {
              button.click()
            }
          })
        }
      }
      this.selectTreasuryTypeTarget.classList.remove('d-none')
      this.currentBalanceAreaTarget.classList.remove('d-none')
      this.chartDataTarget.classList.remove('d-none')
      this.balanceOptionTarget.classList.remove('d-none')
      this.activeProposalSwitchAreaTarget.classList.add('d-none')
    } else {
      this.treasuryToggleAreaTarget.classList.add('d-none')
      this.currentBalanceAreaTarget.classList.add('d-none')
      this.selectTreasuryTypeTarget.classList.add('d-none')
      if (this.isSummaryReport() || this.isAuthorListGroup()) {
        this.activeProposalSwitchAreaTarget.classList.remove('d-none')
      } else {
        this.activeProposalSwitchAreaTarget.classList.add('d-none')
      }
      if (!this.isDomainType()) {
        this.outgoingExpTarget.classList.add('d-none')
        this.treasuryChartTarget.classList.add('d-none')
      } else {
        this.outgoingExpTarget.classList.remove('d-none')
        this.outgoingExpTarget.classList.add('mt-2')
        this.outgoingExpTarget.innerHTML = '*Unaccounted (Est): Unaccounted expenditure is calculated based on actual Treasury expenditures.'
      }
    }

    if (this.isDomainType() || this.settings.type === 'treasury') {
      this.groupByTarget.classList.remove('d-none')
      this.treasuryChartTitleTarget.classList.remove('d-none')
      this.treasuryTypeTitleTarget.classList.remove('d-none')
      if (this.settings.type === 'treasury') {
        this.typeSelectorTarget.classList.remove('d-none')
        this.treasuryChartTitleTarget.textContent = 'Treasury IO Chart'
        this.weekGroupBtnTarget.classList.remove('d-none')
        this.dayGroupBtnTarget.classList.remove('d-none')
        this.blockGroupBtnTarget.classList.remove('d-none')
        // show some option on group and zoom
        this.weekZoomBtnTarget.classList.remove('d-none')
        this.dayZoomBtnTarget.classList.remove('d-none')
        // change domain on radio button label
        this.sentRadioLabelTarget.textContent = 'Sent'
        this.receivedRadioLabelTarget.textContent = 'Received'
        // hide net radio button
        this.netSelectRadioTarget.classList.remove('d-none')
      } else {
        this.typeSelectorTarget.classList.add('d-none')
        this.treasuryChartTitleTarget.textContent = 'Domains Chart Data'
        this.treasuryTypeTitleTarget.textContent = 'Domains Spending Data'
        this.weekGroupBtnTarget.classList.add('d-none')
        this.dayGroupBtnTarget.classList.add('d-none')
        this.blockGroupBtnTarget.classList.add('d-none')
        // hide some option on group and zoom
        this.weekZoomBtnTarget.classList.add('d-none')
        this.dayZoomBtnTarget.classList.add('d-none')
        // change domain on radio button label
        this.sentRadioLabelTarget.textContent = 'Development'
        this.receivedRadioLabelTarget.textContent = 'Marketing'
        // hide net radio button
        this.netSelectRadioTarget.classList.add('d-none')
      }
    } else {
      this.groupByTarget.classList.add('d-none')
      this.treasuryChartTitleTarget.classList.add('d-none')
      this.treasuryTypeTitleTarget.classList.add('d-none')
    }

    // if treasury, get table content in other area
    if (this.settings.type !== 'treasury') {
      this.reportTarget.innerHTML = this.createTableContent()
    }

    // handler for group domains and authors
    if (this.settings.type === '' || this.settings.type === 'proposal' || this.settings.type === 'summary') {
      if (this.settings.pgroup === 'domains') {
        this.treasuryChartTarget.classList.remove('d-none')
        // check chart type. If current is balance, change to amountflow
        if (this.settings.chart === 'balance' || this.chartType === 'balance') {
          this.settings.chart = 'amountflow'
          this.optionsTarget.value = 'amountflow'
        }
        this.initializeChart()
        this.drawGraph()
        // hide balance select option
        this.balanceOptionTarget.classList.add('d-none')
        this.chartDataTarget.classList.add('d-none')
        if (redrawChart && ctrl.zoomButtons) {
          redrawChart = false
          ctrl.zoomButtons.forEach((button) => {
            if (button.name === 'all') {
              button.click()
            }
          })
        }
      }
    }
    if (this.settings.type === '' || this.settings.type === 'proposal' || this.settings.type === 'summary') {
      if (this.settings.pgroup === '' || this.settings.pgroup === 'proposals' || this.settings.pgroup === 'authors' || this.settings.pgroup === 'domains') {
        const tableWidthStr = $('#reportTable thead').css('width').replace('px', '')
        const tableWidth = parseFloat(tableWidthStr.trim())
        const parentContainerWidthStr = $('#repotParentContainer').css('width').replace('px', '')
        const parentContainerWidth = parseFloat(parentContainerWidthStr.trim())
        let hideScroller = false
        if (tableWidth < parentContainerWidth + 5) {
          $('#scroller').addClass('d-none')
          hideScroller = true
        } else {
          $('#scroller').removeClass('d-none')
        }
        this.reportTarget.classList.add('proposal-table-padding')
        let widthFinal = $('#reportTable thead').css('width')
        if (widthFinal !== '' && (this.settings.pgroup === 'authors' || this.settings.pgroup === 'domains') && this.settings.ptype === 'list') {
          let width = parseFloat(widthFinal.replaceAll('px', ''))
          width += 30
          widthFinal = width + 'px'
          this.searchBoxTarget.classList.add('searchbox-align')
        } else {
          this.searchBoxTarget.classList.remove('searchbox-align')
        }
        $('#reportTable').css('width', widthFinal)
        $('html').css('overflow-x', 'hidden')
        // set overflow class
        $('#containerReportTable').addClass('of-x-hidden')
        $('#containerBody').addClass('of-x-hidden')
        $('#scrollerLong').css('width', (tableWidth + 25) + 'px')
        // set scroller width fit with container width
        $('#scroller').css('width', $('#repotParentContainer').css('width'))
        if (this.isMobile()) {
          $('#containerBody').css('overflow', 'scroll')
          this.reportTarget.classList.remove('proposal-table-padding')
          $('#scroller').addClass('d-none')
        } else {
          this.reportTarget.classList.add('proposal-table-padding')
          if (!hideScroller) {
            $('#scroller').removeClass('d-none')
          }
        }
        if (((this.settings.type === 'proposal' || this.settings.type === '') && this.settings.pgroup === 'proposals') || (this.settings.pgroup === 'authors' && this.settings.ptype !== 'list')) {
          // handler for scroll default
          if (this.settings.psort === 'oldest') {
            if (this.settings.tsort === 'newest') {
              $('#scroller').scrollLeft(tableWidth)
            } else {
              $('#scroller').scrollLeft(0)
            }
          } else {
            if (this.settings.tsort === 'newest') {
              $('#scroller').scrollLeft(0)
            } else {
              $('#scroller').scrollLeft(tableWidth)
            }
          }
        }
      } else {
        $('#scroller').addClass('d-none')
      }
    } else {
      $('#reportTable').css('width', 'auto')
      $('#scroller').scrollLeft(0)
      $('#scroller').addClass('d-none')
      $('html').css('overflow-x', '')
    }
  }

  createTableContent () {
    if ((this.settings.type === '' || this.settings.type === 'summary' || this.settings.type === 'proposal') &&
      this.settings.pgroup !== 'proposals' && this.settings.pgroup !== '') {
      if (this.settings.pgroup === 'domains') {
        return this.createDomainTable(responseData)
      }
      if (this.settings.pgroup === 'authors') {
        if (this.settings.ptype === 'list') {
          return this.createAuthorTable(responseData)
        } else {
          return this.createMonthAuthorTable(responseData)
        }
      }
      return ''
    }
    switch (this.settings.type) {
      case 'summary':
        return this.createSummaryTable(responseData)
      default:
        return this.createProposalTable(responseData)
    }
  }

  sortByCreateDate () {
    if (this.settings.type === 'treasury' || this.isDomainType()) {
      this.settings.tsort = this.settings.tsort === 'oldest' ? 'newest' : 'oldest'
      this.treasuryTSort = this.settings.tsort
    } else {
      this.settings.tsort = (this.settings.tsort === 'oldest' || !this.settings.tsort || this.settings.tsort === '') ? 'newest' : 'oldest'
      this.proposalTSort = this.settings.tsort
    }
    if (this.settings.type === 'treasury' || this.isDomainType()) {
      this.settings.stype = ''
    }
    this.createReportTable(false)
  }

  sortByDate () {
    this.settings.psort = (this.settings.psort === 'newest' || !this.settings.psort || this.settings.psort === '') ? 'oldest' : 'newest'
    this.createReportTable(false)
  }

  sortByStartDate () {
    this.sortByType('startdt')
  }

  sortByPName () {
    this.sortByType('pname')
  }

  sortByIncoming () {
    this.sortByType('incoming')
  }

  sortByRate () {
    this.sortByType('rate')
  }

  sortByOutgoing () {
    this.sortByType('outgoing')
  }

  sortByNet () {
    this.sortByType('net')
  }

  sortByBalance () {
    this.sortByType('balance')
  }

  sortSpentPercent () {
    this.sortByType('percent')
  }

  sortByOutgoingEst () {
    this.sortByType('outest')
  }

  sortByDomain () {
    this.sortByType('domain')
  }

  sortByAuthor () {
    this.sortByType('author')
  }

  sortByPNum () {
    this.sortByType('pnum')
  }

  sortByBudget () {
    this.sortByType('budget')
  }

  sortByDays () {
    this.sortByType('days')
  }

  sortByAvg () {
    this.sortByType('avg')
  }

  sortByDomainItem (e) {
    this.sortByType(e.params.domain)
  }

  sortByDomainTotal () {
    this.sortByType('total')
  }

  sortByUnaccounted () {
    this.sortByType('unaccounted')
  }

  sortByTotalSpent () {
    this.sortByType('spent')
  }

  sortByRemaining () {
    this.sortByType('remaining')
  }

  sortByEndDate () {
    this.sortByType('enddt')
  }

  sortByType (type) {
    this.settings.stype = type
    this.settings.order = this.settings.order === 'esc' ? 'desc' : 'esc'
    this.createReportTable(false)
  }

  getTreasuryYearlyData (summary) {
    const dataMap = new Map()
    const yearArr = []
    for (let i = 0; i < summary.length; i++) {
      const item = summary[i]
      const month = item.month
      if (month && month !== '') {
        const timeArr = month.split('-')
        const year = timeArr[0]
        if (!yearArr.includes(year)) {
          yearArr.push(year)
        }
        let monthStr = ''
        if (timeArr[1].charAt(0) === '0') {
          monthStr = timeArr[1].substring(1, timeArr[1].length)
        } else {
          monthStr = timeArr[1]
        }
        const monthInt = parseInt(monthStr, 0)
        let monthObj = {}
        if (dataMap.has(year)) {
          monthObj = dataMap.get(year)
          monthObj.invalue += item.invalue
          monthObj.invalueUSD += item.invalueUSD
          monthObj.outvalue += item.outvalue
          monthObj.outvalueUSD += item.outvalueUSD
          monthObj.difference += item.difference
          monthObj.differenceUSD += item.differenceUSD
          monthObj.total += item.total
          monthObj.totalUSD += item.totalUSD
          monthObj.outEstimate += item.outEstimate
          monthObj.outEstimateUsd += item.outEstimateUsd
          monthObj.monthPrice += item.monthPrice
          monthObj.count += 1
          if (monthInt > monthObj.monthInt) {
            monthObj.monthInt = monthInt
            monthObj.creditLink = item.creditLink
            monthObj.debitLink = item.debitLink
            monthObj.balance = item.balance
            monthObj.balanceUSD = item.balanceUSD
          }
        } else {
          monthObj.month = year
          monthObj.invalue = item.invalue
          monthObj.invalueUSD = item.invalueUSD
          monthObj.outvalue = item.outvalue
          monthObj.outvalueUSD = item.outvalueUSD
          monthObj.difference = item.difference
          monthObj.differenceUSD = item.differenceUSD
          monthObj.total = item.total
          monthObj.totalUSD = item.totalUSD
          monthObj.outEstimate = item.outEstimate
          monthObj.outEstimateUsd = item.outEstimateUsd
          monthObj.monthPrice = item.monthPrice
          monthObj.count = 1
          monthObj.monthInt = monthInt
          monthObj.balance = item.balance
          monthObj.balanceUSD = item.balanceUSD
          monthObj.debitLink = item.debitLink
          monthObj.creditLink = item.creditLink
        }
        dataMap.set(year, monthObj)
      }
    }
    const result = []
    yearArr.forEach((year) => {
      const tempResultItem = dataMap.get(year)
      if (tempResultItem.count > 0) {
        tempResultItem.monthPrice = tempResultItem.monthPrice / tempResultItem.count
      }
      result.push(tempResultItem)
    })
    return result
  }

  getProposalYearlyData (data) {
    const result = {}
    result.allSpent = data.allSpent
    result.allBudget = data.allBudget
    result.proposalList = data.proposalList
    result.domainList = data.domainList
    result.summary = data.summary

    const dataMap = new Map()
    const yearArr = []
    data.report.forEach((report) => {
      const month = report.month
      if (month && month !== '') {
        const year = month.split('/')[0]
        if (!yearArr.includes(year)) {
          yearArr.push(year)
        }
        let monthObj = {}
        if (dataMap.has(year)) {
          monthObj = dataMap.get(year)
          monthObj.total += report.total
          for (let i = 0; i < monthObj.allData.length; i++) {
            monthObj.allData[i].expense += report.allData[i].expense
          }
          for (let i = 0; i < monthObj.domainData.length; i++) {
            monthObj.domainData[i].expense += report.domainData[i].expense
          }
        } else {
          monthObj.total = report.total
          monthObj.month = year
          monthObj.allData = []
          monthObj.domainData = []
          for (let i = 0; i < report.allData.length; i++) {
            const item = report.allData[i]
            const allDataItem = {}
            allDataItem.token = item.token
            allDataItem.name = item.name
            allDataItem.expense = item.expense
            allDataItem.domain = item.domain
            monthObj.allData.push(allDataItem)
          }
          for (let i = 0; i < report.domainData.length; i++) {
            const item = report.domainData[i]
            const domainDataItem = {}
            domainDataItem.domain = item.domain
            domainDataItem.expense = item.expense
            monthObj.domainData.push(domainDataItem)
          }
          dataMap.set(year, monthObj)
        }
      }
    })
    result.report = []
    yearArr.forEach((year) => {
      result.report.push(dataMap.get(year))
    })
    return result
  }

  createProposalTable (data) {
    if (!data.report) {
      return ''
    }

    let handlerData = data
    if (this.settings.interval === 'year') {
      handlerData = this.getProposalYearlyData(data)
    }

    if (handlerData.report.length < 1) {
      this.nodataTarget.classList.remove('d-none')
      this.reportTarget.classList.add('d-none')
      return
    }
    this.nodataTarget.classList.add('d-none')
    this.reportTarget.classList.remove('d-none')

    let thead = '<thead><tr class="text-secondary finance-table-header">' +
      '<th class="text-center ps-0 month-col border-right-grey report-first-header head-first-cell">' +
      '<div class="c1"><span data-action="click->financereport#sortByDate" class="homeicon-swap vertical-sort"></span></div><div class="c2"><span id="sortCreateDate" data-action="click->financereport#sortByCreateDate" class="homeicon-swap horizontal-sort"></span></div></th>' +
      '###' +
      '<th class="text-right ps-0 fw-600 month-col ta-center border-left-grey report-last-header va-mid">Total</th>' +
      '</tr></thead>'
    let tbody = '<tbody>###</tbody>'

    let headList = ''
    const proposalTokenMap = data.proposalTokenMap
    for (let i = 0; i < handlerData.report.length; i++) {
      const index = this.settings.tsort === 'newest' ? i : (handlerData.report.length - i - 1)
      const report = handlerData.report[index]
      const timeParam = this.getFullTimeParam(report.month, '/')

      if (this.settings.interval === 'year') {
        headList += `<th class="text-center fw-600 pb-30i fs-13i table-header-sticky va-mid" id="${this.settings.interval + ';' + report.month}">`
        headList += '<div class="d-flex justify-content-center">'
        headList += `<a class="link-hover-underline fs-13i" style="text-align: right; width: 80px;" href="${'/finance-report/detail?type=' + this.settings.interval + '&time=' + (timeParam === '' ? report.month : timeParam)}">${report.month.replace('/', '-')}`
        headList += '</a></div></th>'
      } else {
        headList += '<th class="text-right fw-600 pb-30i fs-13i ps-2 pe-2 table-header-sticky va-mid" ' +
          `id="${this.settings.interval + ';' + report.month}" ` +
          `><a class="link-hover-underline fs-13i" href="${'/finance-report/detail?type=' + this.settings.interval + '&time=' + (timeParam === '' ? report.month : timeParam)}"><span class="d-block pr-5">${report.month.replace('/', '-')}</span></a></th>`
      }
    }
    thead = thead.replace('###', headList)

    let bodyList = ''
    for (let i = 0; i < handlerData.proposalList.length; i++) {
      const index = this.settings.psort === 'oldest' ? (handlerData.proposalList.length - i - 1) : i
      const proposal = handlerData.proposalList[index]
      let token = ''
      if (proposalTokenMap[proposal] && proposalTokenMap[proposal] !== '') {
        token = proposalTokenMap[proposal]
      }
      bodyList += `<tr class="odd-even-row"><td class="text-center fs-13i border-right-grey report-first-data"><a href="${'/finance-report/detail?type=proposal&token=' + token}" data-turbolinks="false" class="link-hover-underline fs-13i d-block ${this.settings.interval === 'year' ? 'proposal-year-title' : 'proposal-title-col'}">${proposal}</a></td>`
      for (let j = 0; j < handlerData.report.length; j++) {
        const tindex = this.settings.tsort === 'newest' ? j : (handlerData.report.length - j - 1)
        const report = handlerData.report[tindex]
        const allData = report.allData[index]
        if (allData.expense > 0) {
          if (this.settings.interval === 'year') {
            bodyList += `<td class="${this.settings.interval === 'year' ? 'text-center' : 'text-right'} fs-13i proposal-content-td va-mid">`
            bodyList += '<div class="d-flex justify-content-center">'
            bodyList += `<span style="text-align: right; width: 80px;">$${humanize.formatToLocalString(allData.expense, 2, 2)}</span>`
            bodyList += '</div>'
          } else {
            bodyList += '<td class="text-right fs-13i proposal-content-td va-mid">'
            bodyList += `$${humanize.formatToLocalString(allData.expense, 2, 2)}`
          }
        } else {
          bodyList += '<td class="text-center fs-13i">'
        }
        bodyList += '</td>'
      }
      bodyList += `<td class="text-right fs-13i fw-600 border-left-grey report-last-data va-mid">$${humanize.formatToLocalString(handlerData.summary[index].budget, 2, 2)}</td></tr>`
    }

    bodyList += '<tr class="finance-table-header last-row-header">' +
      '<td class="text-center fw-600 fs-13i report-first-header va-mid">Total</td>'
    for (let i = 0; i < handlerData.report.length; i++) {
      const index = this.settings.tsort === 'newest' ? i : (handlerData.report.length - i - 1)
      const report = handlerData.report[index]
      if (this.settings.interval === 'year') {
        bodyList += '<td class="text-center fw-600 fs-13i va-mid">'
        bodyList += '<div class="d-flex justify-content-center">'
        bodyList += `<span style="text-align: right; width: 80px;">$${humanize.formatToLocalString(report.total, 2, 2)}</span>`
        bodyList += '</div>'
        bodyList += '</td>'
      } else {
        bodyList += `<td class="text-right fw-600 fs-13i va-mid">$${humanize.formatToLocalString(report.total, 2, 2)}</td>`
      }
    }

    bodyList += `<td class="text-right fw-600 fs-13i report-last-header va-mid">$${humanize.formatToLocalString(handlerData.allSpent, 2, 2)}</td></tr>`

    tbody = tbody.replace('###', bodyList)
    return thead + tbody
  }

  createMonthAuthorTable (data) {
    if (!data.report) {
      return ''
    }

    const handlerData = data
    if (handlerData.report.length < 1) {
      this.nodataTarget.classList.remove('d-none')
      this.reportTarget.classList.add('d-none')
      return
    }
    this.nodataTarget.classList.add('d-none')
    this.reportTarget.classList.remove('d-none')

    let thead = '<thead><tr class="text-secondary finance-table-header">' +
      '<th class="text-center ps-0 month-col border-right-grey report-first-header head-first-cell">' +
      '<div class="c1"><span data-action="click->financereport#sortByDate" class="homeicon-swap vertical-sort"></span></div><div class="c2"><span data-action="click->financereport#sortByCreateDate" class="homeicon-swap horizontal-sort"></span></div></th>' +
      '###' +
      '<th class="text-right ps-0 fw-600 month-col ta-center border-left-grey report-last-header va-mid">Total</th>' +
      '</tr></thead>'
    let tbody = '<tbody>###</tbody>'

    let headList = ''
    for (let i = 0; i < handlerData.report.length; i++) {
      const index = this.settings.tsort === 'newest' ? i : (handlerData.report.length - i - 1)
      const report = handlerData.report[index]
      const timeParam = this.getFullTimeParam(report.month, '/')

      if (this.settings.interval === 'year') {
        headList += `<th class="text-center fw-600 pb-30i fs-13i table-header-sticky va-mid" id="${this.settings.interval + ';' + report.month}">`
        headList += '<div class="d-flex justify-content-center">'
        headList += `<a class="link-hover-underline fs-13i" data-turbolinks="false" style="text-align: right; width: 80px;" href="${'/finance-report/detail?type=' + this.settings.interval + '&time=' + (timeParam === '' ? report.month : timeParam)}">${report.month.replace('/', '-')}`
        headList += '</a></div></th>'
      } else {
        headList += '<th class="text-right fw-600 pb-30i fs-13i ps-3 pr-3 table-header-sticky va-mid" ' +
          `id="${this.settings.interval + ';' + report.month}" ` +
          `><a class="link-hover-underline fs-13i" href="${'/finance-report/detail?type=' + this.settings.interval + '&time=' + (timeParam === '' ? report.month : timeParam)}"><span class="d-block pr-5">${report.month.replace('/', '-')}</span></a></th>`
      }
    }
    thead = thead.replace('###', headList)

    let bodyList = ''
    for (let i = 0; i < handlerData.authorReport.length; i++) {
      const index = this.settings.psort === 'oldest' ? (handlerData.authorReport.length - i - 1) : i
      const author = handlerData.authorReport[index]
      const budget = author.budget
      bodyList += `<tr class="odd-even-row"><td class="text-center fs-13i border-right-grey report-first-data"><a data-turbolinks="false" href="/finance-report/detail?type=owner&name=${author.name}" class="link-hover-underline fw-600 fs-13i d-block ${this.settings.interval === 'year' ? 'proposal-year-title' : 'proposal-title-col'}">${author.name}</a></td>`
      for (let j = 0; j < handlerData.report.length; j++) {
        const tindex = this.settings.tsort === 'newest' ? j : (handlerData.report.length - j - 1)
        const report = handlerData.report[tindex]
        const expense = this.getAuthorExpense(author.name, report.authorData)
        if (expense > 0) {
          if (this.settings.interval === 'year') {
            bodyList += `<td class="${this.settings.interval === 'year' ? 'text-center' : 'text-right'} fs-13i proposal-content-td va-mid">`
            bodyList += '<div class="d-flex justify-content-center">'
            bodyList += `<span style="text-align: right; width: 80px;">$${humanize.formatToLocalString(expense, 2, 2)}</span>`
            bodyList += '</div>'
          } else {
            bodyList += '<td class="text-right fs-13i proposal-content-td va-mid">'
            bodyList += `$${humanize.formatToLocalString(expense, 2, 2)}`
          }
        } else {
          bodyList += '<td class="text-center fs-13i">'
        }
        bodyList += '</td>'
      }
      bodyList += `<td class="text-right fs-13i fw-600 border-left-grey report-last-data va-mid">$${humanize.formatToLocalString(budget, 2, 2)}</td></tr>`
    }

    bodyList += '<tr class="finance-table-header last-row-header">' +
      '<td class="text-center fw-600 fs-13i report-first-header va-mid">Total</td>'
    for (let i = 0; i < handlerData.report.length; i++) {
      const index = this.settings.tsort === 'newest' ? i : (handlerData.report.length - i - 1)
      const report = handlerData.report[index]
      if (this.settings.interval === 'year') {
        bodyList += '<td class="text-center fw-600 fs-13i va-mid">'
        bodyList += '<div class="d-flex justify-content-center">'
        bodyList += `<span style="text-align: right; width: 80px;">$${humanize.formatToLocalString(report.total, 2, 2)}</span>`
        bodyList += '</div>'
        bodyList += '</td>'
      } else {
        bodyList += `<td class="text-right fw-600 fs-13i va-mid">$${humanize.formatToLocalString(report.total, 2, 2)}</td>`
      }
    }

    bodyList += `<td class="text-right fw-600 fs-13i report-last-header va-mid">$${humanize.formatToLocalString(handlerData.allSpent, 2, 2)}</td></tr>`

    tbody = tbody.replace('###', bodyList)
    return thead + tbody
  }

  getAuthorExpense (author, authorData) {
    let expense = 0
    authorData.forEach(tmp => {
      if (tmp.author === author) {
        expense = tmp.expense
      }
    })
    return expense
  }

  createSummaryTable (data) {
    if (!data.report) {
      return ''
    }
    if (data.summary.length < 1) {
      this.nodataTarget.classList.remove('d-none')
      this.reportTarget.classList.add('d-none')
      return
    }
    this.nodataTarget.classList.add('d-none')
    this.reportTarget.classList.remove('d-none')

    if (!this.settings.stype || this.settings.stype === '') {
      this.settings.stype = this.defaultSettings.stype
    }

    if (!this.settings.order || this.settings.order === '') {
      this.settings.order = this.defaultSettings.order
    }

    const thead = '<thead>' +
      '<tr class="text-secondary finance-table-header">' +
      '<th class="va-mid text-center month-col fw-600 proposal-name-col"><label class="cursor-pointer" data-action="click->financereport#sortByPName">Name</label>' +
      `<span data-action="click->financereport#sortByPName" class="${(this.settings.stype === 'pname' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'pname' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-center px-3 month-col fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByDomain">Domain</label>' +
      `<span data-action="click->financereport#sortByDomain" class="${(this.settings.stype === 'domain' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'domain' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-center px-3 month-col fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByAuthor">Author</label>' +
      `<span data-action="click->financereport#sortByAuthor" class="${(this.settings.stype === 'author' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'author' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-center px-3 fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByStartDate">Start Date</label>' +
      `<span data-action="click->financereport#sortByStartDate" class="${(this.settings.stype === 'startdt' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${(!this.settings.stype || this.settings.stype === '' || this.settings.stype === 'startdt') ? '' : 'c-grey-4'} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-center px-3 fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByEndDate">End Date</label>' +
      `<span data-action="click->financereport#sortByEndDate" class="${(this.settings.stype === 'enddt' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'enddt' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right px-3 fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByBudget">Budget</label>' +
      `<span data-action="click->financereport#sortByBudget" class="${(this.settings.stype === 'budget' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'budget' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-center fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByDays">Days</label>' +
      `<span data-action="click->financereport#sortByDays" class="${(this.settings.stype === 'days' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'days' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-center fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByAvg">Monthly Avg (Est)</label>' +
      `<span data-action="click->financereport#sortByAvg" class="${(this.settings.stype === 'avg' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'avg' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right px-3 fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByTotalSpent">Total Spent (Est)</label>' +
      `<span data-action="click->financereport#sortByTotalSpent" class="${(this.settings.stype === 'spent' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'spent' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right px-3 fw-600 pr-10i"><label class="cursor-pointer" data-action="click->financereport#sortByRemaining">Total Remaining (Est)</label>' +
      `<span data-action="click->financereport#sortByRemaining" class="${(this.settings.stype === 'remaining' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'remaining' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '</tr></thead>'
    let tbody = '<tbody>###</tbody>'
    let bodyList = ''
    const proposalTokenMap = data.proposalTokenMap
    // Handler sort before display data
    // sort by param
    const summaryList = this.sortSummary(data.summary)
    let totalBudget = 0.0
    let totalSpent = 0.0
    // create tbody content
    for (let i = 0; i < summaryList.length; i++) {
      const summary = summaryList[i]
      if (this.settings.active && summary.totalRemaining === 0.0) {
        continue
      }
      let token = ''
      if (proposalTokenMap[summary.name] && proposalTokenMap[summary.name] !== '') {
        token = proposalTokenMap[summary.name]
      }
      const lengthInDays = this.getLengthInDay(summary)
      let monthlyAverage = summary.budget / lengthInDays
      if (lengthInDays < 30) {
        monthlyAverage = summary.budget
      } else {
        monthlyAverage = monthlyAverage * 30
      }
      totalBudget += summary.budget
      totalSpent += summary.totalSpent
      bodyList += `<tr class="${summary.totalRemaining === 0.0 ? 'proposal-summary-row' : 'summary-active-row'}">` +
        `<td class="va-mid text-center fs-13i proposal-name-column"><a href="${'/finance-report/detail?type=proposal&token=' + token}" class="link-hover-underline fs-13i">${summary.name}</a></td>` +
        `<td class="va-mid text-center px-3 fs-13i"><a href="${'/finance-report/detail?type=domain&name=' + summary.domain}" class="link-hover-underline fs-13i">${summary.domain.charAt(0).toUpperCase() + summary.domain.slice(1)}</a></td>` +
        `<td class="va-mid text-center px-3 fs-13i"><a href="${'/finance-report/detail?type=owner&name=' + summary.author}" class="link-hover-underline fs-13i">${summary.author}</a></td>` +
        `<td class="va-mid text-center px-3 fs-13i"><label class="date-column">${summary.start}</label></td>` +
        `<td class="va-mid text-center px-3 fs-13i"><label class="date-column">${summary.end}</label></td>` +
        `<td class="va-mid text-right px-3 fs-13i">$${humanize.formatToLocalString(summary.budget, 2, 2)}</td>` +
        `<td class="va-mid text-right px-3 fs-13i">${lengthInDays}</td>` +
        `<td class="va-mid text-right px-3 fs-13i"><p class="est-column">$${humanize.formatToLocalString(monthlyAverage, 2, 2)}</p></td>` +
        `<td class="va-mid text-right px-3 fs-13i"><p class="est-column">${summary.totalSpent > 0 ? '$' + humanize.formatToLocalString(summary.totalSpent, 2, 2) : ''}</p></td>` +
        `<td class="va-mid text-right px-3 fs-13i pr-10i"><p class="remaining-est-column">${summary.totalRemaining > 0 ? '$' + humanize.formatToLocalString(summary.totalRemaining, 2, 2) : ''}</p></td>` +
        '</tr>'
    }

    bodyList += '<tr class="finance-table-header last-row-header">' +
      '<td class="va-mid text-center fw-600 fs-15i" colspan="5">Total</td>' +
      `<td class="va-mid text-right px-3 fw-600 fs-15i">$${humanize.formatToLocalString(totalBudget, 2, 2)}</td>` +
      '<td></td><td></td>' +
      `<td class="va-mid text-right px-3 fw-600 fs-15i">$${humanize.formatToLocalString(totalSpent, 2, 2)}</td>` +
      `<td class="va-mid text-right px-2 fw-600 fs-15i">$${humanize.formatToLocalString(totalBudget - totalSpent, 2, 2)}</td>` +
      '</tr>'

    tbody = tbody.replace('###', bodyList)
    return thead + tbody
  }

  changeYear (e) {
    if (!e.target.value || e.target.value === '') {
      return
    }
    this.settings.year = e.target.value
    this.calculate()
  }

  getLengthInDay (summary) {
    const start = Date.parse(summary.start)
    const end = Date.parse(summary.end)
    const oneDay = 24 * 60 * 60 * 1000

    return Math.round(Math.abs((end - start) / oneDay))
  }

  sortAuthorData (authorList) {
    switch (this.settings.stype) {
      case 'budget':
        return this.sortSummaryByBudget(authorList)
      case 'spent':
        return this.sortAuthorByTotalReceived(authorList)
      case 'remaining':
        return this.sortAuthorByRemaining(authorList)
      case 'pnum':
        return this.sortAuthorByProposalNum(authorList)
      default:
        return this.sortSummaryByName(authorList)
    }
  }

  sortAuthorByProposalNum (authorList) {
    if (!authorList) {
      return
    }
    const _this = this
    authorList.sort(function (a, b) {
      if (a.proposals > b.proposals) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (a.proposals < b.proposals) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return authorList
  }

  sortAuthorByRemaining (authorList) {
    if (!authorList) {
      return
    }
    const _this = this
    authorList.sort(function (a, b) {
      if (a.totalRemaining > b.totalRemaining) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (a.totalRemaining < b.totalRemaining) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return authorList
  }

  sortAuthorByTotalReceived (authorList) {
    if (!authorList) {
      return
    }
    const _this = this
    authorList.sort(function (a, b) {
      if (a.totalReceived > b.totalReceived) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (a.totalReceived < b.totalReceived) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return authorList
  }

  sortDomains (domainDataList) {
    let result = []
    if (!domainDataList || domainDataList.length === 0) {
      return result
    }

    // if not sort by data column, sort by month, year
    if (!this.settings.stype || this.settings.stype === '') {
      for (let i = 0; i < domainDataList.length; i++) {
        const sort = this.settings.tsort
        result.push(sort === 'oldest' ? domainDataList[domainDataList.length - i - 1] : domainDataList[i])
      }
      return result
    }

    result = Array.from(domainDataList)
    const _this = this
    result.sort(function (a, b) {
      let aData = null
      let bData = null
      if (_this.settings.stype === 'total') {
        aData = a.total
        bData = b.total
      } else if (_this.settings.stype === 'unaccounted') {
        aData = a.unaccounted
        bData = b.unaccounted
      } else {
        a.domainData.forEach((aDomainData) => {
          if (_this.settings.stype === aDomainData.domain) {
            aData = aDomainData.expense
          }
        })
        b.domainData.forEach((bDomainData) => {
          if (_this.settings.stype === bDomainData.domain) {
            bData = bDomainData.expense
          }
        })
      }
      if (aData > bData) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (aData < bData) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return result
  }

  sortTreasury (treasuryList) {
    let result = []
    if (!treasuryList) {
      return result
    }
    // if not sort by data column, sort by month, year
    if (!this.settings.stype || this.settings.stype === '') {
      for (let i = 0; i < treasuryList.length; i++) {
        const sort = this.settings.tsort
        result.push(sort === 'newest' ? treasuryList[i] : treasuryList[treasuryList.length - i - 1])
      }
      return result
    }

    result = Array.from(treasuryList)
    const _this = this
    result.sort(function (a, b) {
      let aData = null
      let bData = null
      switch (_this.settings.stype) {
        case 'outgoing':
          aData = a.outvalue
          bData = b.outvalue
          break
        case 'net':
          aData = a.outvalue > a.invalue ? 0 - a.difference : a.difference
          bData = b.outvalue > b.invalue ? 0 - b.difference : b.difference
          break
        case 'outest':
          aData = a.outEstimate
          bData = b.outEstimate
          break
        case 'rate':
          aData = a.monthPrice
          bData = b.monthPrice
          break
        case 'balance':
          aData = a.balance
          bData = b.balance
          break
        case 'percent':
          aData = 0
          bData = 0
          if (a.outvalue > 0) {
            aData = 100 * 1e8 * a.outEstimate / a.outvalue
          }
          if (b.outvalue > 0) {
            bData = 100 * 1e8 * b.outEstimate / b.outvalue
          }
          break
        default:
          aData = a.invalue
          bData = b.invalue
      }
      if (aData > bData) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (aData < bData) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return result
  }

  sortSummary (summary) {
    switch (this.settings.stype) {
      case 'pname':
        return this.sortSummaryByName(summary)
      case 'domain':
        return this.sortSummaryByDomain(summary)
      case 'author':
        return this.sortSummaryByAuthor(summary)
      case 'budget':
        return this.sortSummaryByBudget(summary)
      case 'spent':
        return this.sortSummaryBySpent(summary)
      case 'remaining':
        return this.sortSummaryByRemaining(summary)
      case 'days':
        return this.sortSummaryByDays(summary)
      case 'avg':
        return this.sortSummaryByAvg(summary)
      case 'enddt':
        return this.sortSummaryByDate(summary, false)
      default:
        return this.sortSummaryByDate(summary, true)
    }
  }

  sortSummaryByDate (summary, isStart) {
    if (!summary) {
      return
    }
    const _this = this
    summary.sort(function (a, b) {
      const date1 = Date.parse(isStart ? a.start : a.end)
      const date2 = Date.parse(isStart ? b.start : b.end)
      if (date1 > date2) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (date1 < date2) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return summary
  }

  sortSummaryByDays (summary) {
    if (!summary) {
      return
    }
    const _this = this
    summary.sort(function (a, b) {
      const lengtha = _this.getLengthInDay(a)
      const lengthb = _this.getLengthInDay(b)
      if (lengtha > lengthb) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (lengtha < lengthb) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return summary
  }

  sortSummaryByAvg (summary) {
    if (!summary) {
      return
    }
    const _this = this
    summary.sort(function (a, b) {
      const avga = (a.budget / _this.getLengthInDay(a)) * 30
      const avgb = (b.budget / _this.getLengthInDay(b)) * 30
      if (avga > avgb) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (avga < avgb) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return summary
  }

  sortSummaryByRemaining (summary) {
    if (!summary) {
      return
    }
    const _this = this
    summary.sort(function (a, b) {
      if (a.totalRemaining > b.totalRemaining) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (a.totalRemaining < b.totalRemaining) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return summary
  }

  sortSummaryBySpent (summary) {
    if (!summary) {
      return
    }
    const _this = this
    summary.sort(function (a, b) {
      if (a.totalSpent > b.totalSpent) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (a.totalSpent < b.totalSpent) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return summary
  }

  sortSummaryByBudget (summary) {
    if (!summary) {
      return
    }
    const _this = this
    summary.sort(function (a, b) {
      if (a.budget > b.budget) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (a.budget < b.budget) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return summary
  }

  sortByStartdt (summary) {
    if (this.settings.order !== 'esc') {
      return summary
    }
    const result = []
    for (let i = summary.length - 1; i >= 0; i--) {
      result.push(summary[i])
    }
    return result
  }

  sortSummaryByDomain (summary) {
    if (!summary) {
      return
    }
    const _this = this
    summary.sort(function (a, b) {
      if (a.domain > b.domain) {
        return _this.settings.order === 'desc' ? -1 : 1
      } else if (a.domain < b.domain) {
        return _this.settings.order === 'desc' ? 1 : -1
      } else {
        if (a.name > b.name) {
          return _this.settings.order === 'desc' ? -1 : 1
        }
        if (a.name < b.name) {
          return _this.settings.order === 'desc' ? 1 : -1
        }
      }
      return 0
    })

    return summary
  }

  sortSummaryByName (summary) {
    if (!summary) {
      return
    }
    const _this = this
    summary.sort(function (a, b) {
      if (a.name > b.name) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (a.name < b.name) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return summary
  }

  sortSummaryByAuthor (summary) {
    if (!summary) {
      return
    }
    const _this = this
    summary.sort(function (a, b) {
      if (a.author > b.author) {
        return _this.settings.order === 'desc' ? -1 : 1
      }
      if (a.author < b.author) {
        return _this.settings.order === 'desc' ? 1 : -1
      }
      return 0
    })

    return summary
  }

  swapItemOnArray (array, index1, index2) {
    const temp = array[index1]
    array[index1] = index2
    array[index2] = temp
  }

  createAuthorTable (data) {
    if (!data.authorReport) {
      return ''
    }

    if (!this.settings.stype || this.settings.stype === '') {
      this.settings.stype = 'author'
    }

    if (!this.settings.order || this.settings.order === '') {
      this.settings.order = this.defaultSettings.order
    }

    const thead = '<thead>' +
      '<tr class="text-secondary finance-table-header">' +
      '<th class="va-mid text-center px-3 month-col fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByAuthor">Author</label>' +
      `<span data-action="click->financereport#sortByAuthor" class="${(this.settings.stype === 'author' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'author' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-center px-3 fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByPNum">Proposals</label>' +
      `<span data-action="click->financereport#sortByPNum" class="${(this.settings.stype === 'pnum' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'pnum' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right px-3 fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByBudget">Total Budget</label>' +
      `<span data-action="click->financereport#sortByBudget" class="${(this.settings.stype === 'budget' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'budget' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right px-3 fw-600"><label class="cursor-pointer" data-action="click->financereport#sortByTotalSpent">Total Received (Est)</label>' +
      `<span data-action="click->financereport#sortByTotalSpent" class="${(this.settings.stype === 'spent' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'spent' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right px-3 fw-600 pr-10i"><label class="cursor-pointer" data-action="click->financereport#sortByRemaining">Total Remaining (Est)</label>' +
      `<span data-action="click->financereport#sortByRemaining" class="${(this.settings.stype === 'remaining' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'remaining' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '</tr></thead>'
    let tbody = '<tbody>###</tbody>'
    let bodyList = ''
    let totalBudget = 0
    let totalSpent = 0
    let totalRemaining = 0
    let totalProposals = 0
    // Handler sort before display data
    const authorList = this.sortAuthorData(data.authorReport)
    // create tbody content
    for (let i = 0; i < authorList.length; i++) {
      const author = authorList[i]
      if (this.settings.active && author.totalRemaining === 0.0) {
        continue
      }
      totalBudget += author.budget
      totalSpent += author.totalReceived
      totalRemaining += author.totalRemaining
      totalProposals += author.proposals
      bodyList += `<tr class="${author.totalRemaining === 0.0 ? 'proposal-summary-row' : 'summary-active-row'}"><td class="va-mid text-center px-3 fs-13i fw-600"><a class="link-hover-underline fs-13i" href="${'/finance-report/detail?type=owner&name=' + author.name}">${author.name}</a></td>`
      bodyList += `<td class="va-mid text-center px-3 fs-13i">${author.proposals}</td>`
      bodyList += `<td class="va-mid text-right px-3 fs-13i">${author.budget > 0 ? '$' + humanize.formatToLocalString(author.budget, 2, 2) : ''}</td>`
      bodyList += `<td class="va-mid text-right px-3 fs-13i">${author.totalReceived > 0 ? '$' + humanize.formatToLocalString(author.totalReceived, 2, 2) : ''}</td>`
      bodyList += `<td class="va-mid text-right px-3 fs-13i">${author.totalRemaining > 0 ? '$' + humanize.formatToLocalString(author.totalRemaining, 2, 2) : ''}</td></tr>`
    }

    bodyList += '<tr class="finance-table-header last-row-header">' +
      '<td class="va-mid text-center px-3 fw-600 fs-15i">Total</td>' +
      `<td class="va-mid text-center px-3 fw-600 fs-15i">${totalProposals}</td>` +
      `<td class="va-mid text-right px-3 fw-600 fs-15i">$${humanize.formatToLocalString(totalBudget, 2, 2)}</td>` +
      `<td class="va-mid text-right px-3 fw-600 fs-15i">$${humanize.formatToLocalString(totalSpent, 2, 2)}</td>` +
      `<td class="va-mid text-right px-3 fw-600 fs-15i">$${humanize.formatToLocalString(totalRemaining, 2, 2)}</td>` +
      '</tr>'
    tbody = tbody.replace('###', bodyList)
    return thead + tbody
  }

  createDomainTable (data) {
    if (!data.report) {
      return ''
    }
    let handlerData = data
    let treasuryData = data.treasurySummary
    if (this.settings.interval === 'year') {
      handlerData = domainYearData != null ? domainYearData : this.getProposalYearlyData(data)
      treasuryData = treasurySummaryData != null ? treasurySummaryData : this.getTreasuryYearlyData(treasuryData)
      this.futureLabelTarget.textContent = 'Years in the future'
    } else {
      this.futureLabelTarget.textContent = 'Months in the future'
    }
    const treasuryDataMap = this.getTreasuryMonthSpentMap(treasuryData)
    handlerData = this.getTreasuryDomainCombined(handlerData, treasuryDataMap)

    if (handlerData.report.length < 1) {
      this.nodataTarget.classList.remove('d-none')
      this.reportTarget.classList.add('d-none')
      return
    }
    this.nodataTarget.classList.add('d-none')
    this.reportTarget.classList.remove('d-none')

    let thead = '<thead><tr class="text-secondary finance-table-header">' +
      `<th class="va-mid text-center ps-0 month-col cursor-pointer" data-action="click->financereport#sortByCreateDate"><span class="${this.settings.tsort === 'oldest' ? 'dcricon-arrow-up' : 'dcricon-arrow-down'} ${this.settings.stype && this.settings.stype !== '' ? 'c-grey-4' : ''} col-sort"></span></th>` +
      '###' +
      '<th class="va-mid text-right fw-600 total-last-col px-3 border-left-grey"><label class="cursor-pointer fs-13i" data-action="click->financereport#sortByDomainTotal">Total (Est)</label>' +
      `<span data-action="click->financereport#sortByDomainTotal" class="${(this.settings.stype === 'total' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'total' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right fw-600 total-last-col px-3 border-left-grey" style="min-width: 160px;"><label class="cursor-pointer fs-13i" data-action="click->financereport#sortByUnaccounted">Unaccounted (Est)</label>' +
      `<span data-action="click->financereport#sortByUnaccounted" class="${(this.settings.stype === 'unaccounted' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'unaccounted' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '</tr></thead>'
    let tbody = '<tbody>###</tbody>'

    let headList = ''
    handlerData.domainList.forEach((domain) => {
      headList += `<th class="va-mid text-right-i domain-content-cell fs-13i px-3 fw-600"><a href="${'/finance-report/detail?type=domain&name=' + domain}" class="link-hover-underline fs-13i">${domain.charAt(0).toUpperCase() + domain.slice(1)} (Est)</a>` +
        `<span data-action="click->financereport#sortByDomainItem" data-financereport-domain-param="${domain}" class="${(this.settings.stype === domain && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== domain ? 'c-grey-4' : ''} col-sort ms-1"></span></th>`
    })
    thead = thead.replace('###', headList)

    let bodyList = ''
    const domainDataMap = new Map()
    // sort before display on table
    const domainList = this.sortDomains(handlerData.report)
    // create tbody content
    for (let i = 0; i < domainList.length; i++) {
      const report = domainList[i]
      const timeParam = this.getFullTimeParam(report.month, '/')
      let isFuture = false
      const timeYearMonth = this.getYearMonthArray(report.month, '/')
      const nowDate = new Date()
      const year = nowDate.getUTCFullYear()
      const month = nowDate.getUTCMonth() + 1
      if (this.settings.interval === 'year') {
        isFuture = timeYearMonth[0] > year
      } else {
        const compareDataTime = timeYearMonth[0] * 12 + timeYearMonth[1]
        const compareNowTime = year * 12 + month
        isFuture = compareDataTime > compareNowTime
      }
      bodyList += `<tr class="odd-even-row ${isFuture ? 'future-row-data' : ''}"><td class="va-mid text-center fs-13i fw-600"><a class="link-hover-underline fs-13i" style="text-align: right; width: 80px;" href="${'/finance-report/detail?type=' + this.settings.interval + '&time=' + (timeParam === '' ? report.month : timeParam)}">${report.month.replace('/', '-')}</a></td>`
      report.domainData.forEach((domainData) => {
        bodyList += `<td class="va-mid text-right-i domain-content-cell pe-4 fs-13i">${domainData.expense > 0 ? '$' + humanize.formatToLocalString(domainData.expense, 2, 2) : ''}</td>`
        if (domainDataMap.has(domainData.domain)) {
          domainDataMap.set(domainData.domain, domainDataMap.get(domainData.domain) + domainData.expense)
        } else {
          domainDataMap.set(domainData.domain, domainData.expense)
        }
      })
      bodyList += `<td class="va-mid text-right fs-13i fw-600 pe-4 border-left-grey">$${humanize.formatToLocalString(report.total, 2, 2)}</td>` +
        `<td class="va-mid text-right fs-13i pe-4">${isFuture ? 'Not due yet' : report.unaccounted === -1 ? 'Not sent' : '$' + humanize.formatToLocalString(report.unaccounted, 2, 2)}</td></tr>`
    }

    bodyList += '<tr class="finance-table-header last-row-header"><td class="text-center fw-600 fs-13i border-right-grey">Total (Est)</td>'
    let totalAll = 0
    handlerData.domainList.forEach((domain) => {
      bodyList += `<td class="va-mid text-right fw-600 fs-13i domain-content-cell pe-4">$${humanize.formatToLocalString(domainDataMap.get(domain), 2, 2)}</td>`
      totalAll += domainDataMap.get(domain)
    })
    bodyList += `<td class="va-mid text-right fw-600 fs-13i border-left-grey pe-4">$${humanize.formatToLocalString(totalAll, 2, 2)}</td>`
    bodyList += '<td class="va-mid text-right fw-600 fs-13i border-left-grey pe-2"></td>'
    bodyList += '</tr>'

    tbody = tbody.replace('###', bodyList)
    return thead + tbody
  }

  getTreasuryDomainCombined (reportData, treasuryDataMap) {
    const report = reportData.report
    if (!report || report.length === 0) {
      return reportData
    }
    for (let i = report.length - 1; i >= 0; i--) {
      const reportItem = report[i]
      const monthFormat = reportItem.month.replace('/', '-')
      reportData.report[i].unaccounted = 0
      if (treasuryDataMap.has(monthFormat)) {
        const treasurySpent = treasuryDataMap.get(monthFormat)
        if (treasurySpent > reportItem.total) {
          reportData.report[i].unaccounted = treasurySpent - reportItem.total
        } else if (treasurySpent === 0) {
          reportData.report[i].unaccounted = -1
        } else {
          reportData.report[i].unaccounted = 0
        }
      }
    }
    return reportData
  }

  getFullTimeParam (timeInput, splitChar) {
    const timeArr = timeInput.split(splitChar)
    let timeParam = ''
    if (timeArr.length === 2) {
      timeParam = timeArr[0] + '_'
      // if month < 10
      if (timeArr[1].charAt(0) === '0') {
        timeParam += timeArr[1].substring(1, timeArr[1].length)
      } else {
        timeParam += timeArr[1]
      }
    }
    return timeParam
  }

  getYearMonthArray (timeInput, splitChar) {
    const timeArr = timeInput.split(splitChar)
    const result = []
    if (timeArr.length < 2) {
      result.push(parseInt(timeArr[0]))
      return result
    }
    result.push(parseInt(timeArr[0]))
    result.push(parseInt(timeArr[1]))
    return result
  }

  createTreasuryTable (data) {
    this.treasuryTypeTitleTarget.textContent = this.settings.ttype === 'legacy' ? 'Admin Treasury' : this.settings.ttype === 'combined' ? 'Combined' : 'Decentralized Treasury'
    if (this.settings.ttype !== 'legacy') {
      this.outgoingExpTarget.classList.remove('d-none')
      this.outgoingExpTarget.classList.remove('mt-2')
      this.outgoingExpTarget.innerHTML = '*Outgoing (Est): This is based on total estimated proposal spends from proposal budgets.<br/>*Dev Spent (Est): Estimated costs covered for proposals.'
    } else {
      this.outgoingExpTarget.classList.add('d-none')
    }
    if (!data.treasurySummary && !data.legacySummary) {
      return
    }
    let treasuryData = this.getTreasuryDataWithType(data)
    // if not init combined, hanlder data
    if (!combinedChartData || !combinedChartYearData) {
      this.handlerDataForCombinedChart(treasuryData)
    }
    if (treasuryData === null) {
      return
    }

    if (this.settings.interval === 'year') {
      treasuryData = this.getTreasuryYearlyData(treasuryData)
      combinedYearData = treasuryData
      this.yearSelectTitleTarget.classList.add('d-none')
      this.yearSelectTarget.classList.add('d-none')
    } else {
      this.yearSelectTitleTarget.classList.remove('d-none')
      this.yearSelectTarget.classList.remove('d-none')
      // init year select options
      this.initYearSelectOptions(treasuryData)
    }
    // filter by year
    if (this.settings.interval !== 'year' && this.settings.year && this.settings.year.toString() !== '0') {
      const tmpData = []
      treasuryData.forEach((treasury) => {
        const yearArr = treasury.month.split('-')
        if (yearArr.length < 2) {
          return
        }
        if (this.settings.year.toString() !== yearArr[0].trim()) {
          return
        }
        tmpData.push(treasury)
      })
      treasuryData = tmpData
    }

    if (this.settings.ttype === 'combined') {
      this.initLegacyBalanceMap(this.settings.interval === 'year' ? this.getTreasuryYearlyData(data.legacySummary) : data.legacySummary, this.settings.interval === 'year' ? combinedYearData : combinedData)
      this.initTreasuryBalanceMap(this.settings.interval === 'year' ? this.getTreasuryYearlyData(data.treasurySummary) : data.treasurySummary, this.settings.interval === 'year' ? combinedYearData : combinedData)
      this.initCombinedBalanceMap(treasuryData)
      this.specialTreasuryTarget.classList.add('d-none')
      this.treasuryTypeRateTarget.classList.remove('d-none')
    } else {
      this.specialTreasuryTarget.classList.remove('d-none')
      this.treasuryTypeRateTarget.classList.add('d-none')
    }
    this.reportTarget.innerHTML = this.createTreasuryLegacyTableContent(treasuryData, data.treasurySummary, data.legacySummary)
  }

  initYearSelectOptions (treasuryData) {
    const yearArr = []
    if (!treasuryData) {
      return
    }
    treasuryData.forEach((treasury) => {
      const timeArr = treasury.month.split('-')
      if (timeArr.length > 1) {
        const year = timeArr[0].trim()
        if (!yearArr.includes(year)) {
          yearArr.push(year)
        }
      }
    })
    let options = `<option name="all" value="0" ${!this.settings.year || this.settings.year === 0 ? 'selected' : ''}>All</option>`
    yearArr.forEach((year) => {
      options += `<option name="name_${year}" value="${year}" ${this.settings.year && this.settings.year.toString() === year ? 'selected' : ''}>${year}</option>`
    })
    this.yearSelectTarget.innerHTML = options
  }

  getTreasuryMonthSpentMap (treasurySummary) {
    const spentMap = new Map()
    if (!treasurySummary || treasurySummary.length < 1) {
      return spentMap
    }
    treasurySummary.forEach((summary) => {
      spentMap.set(summary.month, summary.outvalueUSD)
    })
    return spentMap
  }

  getTreasuryValueMapByMonth (treasurySummary) {
    const summaryMap = new Map()
    const result = new Map()
    treasurySummary.forEach((summary) => {
      summaryMap.set(summary.month, summary.balanceUSD)
    })
    const now = new Date()
    const month = now.getUTCMonth() + 1
    const year = now.getUTCFullYear()
    const nowCompare = this.settings.interval === 'year' ? year : year * 12 + month
    const lowestTime = treasurySummary[treasurySummary.length - 1].month
    const lowestArr = lowestTime.split('-')
    if (this.settings.interval !== 'year' && lowestArr.length < 2) {
      return result
    }
    const lowestYear = Number(lowestArr[0])
    let lowestMonth = 0
    if (this.settings.interval !== 'year') {
      // if month < 10
      let lowestMonthStr = ''
      if (lowestArr[1].charAt(0) === '0') {
        lowestMonthStr = lowestArr[1].substring(1, lowestArr[1].length)
      } else {
        lowestMonthStr = lowestArr[1]
      }
      lowestMonth = Number(lowestMonthStr)
    }

    const lowestCompare = this.settings.interval === 'year' ? lowestYear : lowestYear * 12 + lowestMonth
    // browse frome lowest to now
    let tempBalance = 0
    for (let i = lowestCompare; i <= nowCompare; i++) {
      let year = i
      let month = 0
      let key = ''
      if (this.settings.interval !== 'year') {
        month = i % 12
        year = (i - month) / 12
        if (month === 0) {
          year -= 1
          month = 12
        }
        key = year + '-' + (month < 10 ? '0' + month : month)
      } else {
        key = year.toString()
      }
      // check key on summary map
      if (summaryMap.has(key)) {
        tempBalance = summaryMap.get(key)
      }
      result.set(key, tempBalance)
    }
    return result
  }

  getTreasuryDataWithType (data) {
    if (this.settings.ttype === 'current') {
      return data.treasurySummary
    }
    if (this.settings.ttype === 'legacy') {
      return data.legacySummary
    }
    if (combinedData !== null) {
      return combinedData
    }
    const _this = this
    // create time map
    const timeArr = []
    // return combined data
    const combinedDataMap = new Map()
    if (data.treasurySummary) {
      data.treasurySummary.forEach((treasury) => {
        timeArr.push(treasury.month)
        // create object and insert to map
        const item = {
          month: treasury.month,
          invalue: treasury.invalue,
          invalueUSD: treasury.invalueUSD,
          outvalue: treasury.outvalue,
          outvalueUSD: treasury.outvalueUSD,
          difference: treasury.difference,
          differenceUSD: treasury.differenceUSD,
          total: treasury.total,
          totalUSD: treasury.totalUSD,
          outEstimate: treasury.outEstimate,
          outEstimateUsd: treasury.outEstimateUsd,
          monthPrice: treasury.monthPrice
        }
        combinedDataMap.set(treasury.month, item)
      })
    }
    if (data.legacySummary) {
      data.legacySummary.forEach((legacy) => {
        if (!timeArr.includes(legacy.month)) {
          timeArr.push(legacy.month)
          // create object and insert to map
          const item = {
            month: legacy.month,
            invalue: legacy.invalue,
            invalueUSD: legacy.invalueUSD,
            outvalue: legacy.outvalue,
            outvalueUSD: legacy.outvalueUSD,
            difference: legacy.difference,
            differenceUSD: legacy.differenceUSD,
            total: legacy.total,
            totalUSD: legacy.totalUSD,
            outEstimate: legacy.outEstimate,
            outEstimateUsd: legacy.outEstimateUsd,
            monthPrice: legacy.monthPrice
          }
          combinedDataMap.set(legacy.month, item)
        } else if (combinedDataMap.has(legacy.month)) {
          // if has in array (in map)
          const item = combinedDataMap.get(legacy.month)
          item.invalue += legacy.invalue
          item.invalueUSD += legacy.invalueUSD
          item.total += legacy.total
          item.totalUSD += legacy.totalUSD
          item.outvalue += legacy.outvalue
          item.outvalueUSD += legacy.outvalueUSD
          item.difference = Math.abs(item.invalue - item.outvalue)
          item.differenceUSD = (item.difference / 100000000) * item.monthPrice
          combinedDataMap.set(legacy.month, item)
        }
      })
    }

    timeArr.sort(function (a, b) {
      const aTimeCompare = _this.getTimeCompare(a)
      const bTimeCompare = _this.getTimeCompare(b)
      if (aTimeCompare > bTimeCompare) {
        return 1
      }
      if (aTimeCompare < bTimeCompare) {
        return -1
      }
      return 0
    })

    const result = []
    let balanceTotal = 0.0
    timeArr.forEach((month) => {
      if (combinedDataMap.has(month)) {
        const dataItem = combinedDataMap.get(month)
        balanceTotal += dataItem.invalue - dataItem.outvalue
        dataItem.balance = balanceTotal
        dataItem.balanceUSD = (balanceTotal / 100000000) * dataItem.monthPrice
        result.push(dataItem)
      }
    })
    const mainResult = []
    for (let i = result.length - 1; i >= 0; i--) {
      mainResult.push(result[i])
    }
    combinedData = mainResult
    return mainResult
  }

  initLegacyBalanceMap (legacyData, combinedData) {
    if (this.settings.interval !== 'year' && adminBalanceMap != null) {
      return adminBalanceMap
    }
    if (this.settings.interval === 'year' && adminYearlyBalanceMap != null) {
      return adminYearlyBalanceMap
    }
    const tempHasDataBalancemap = new Map()
    legacyData.forEach(summary => {
      tempHasDataBalancemap.set(summary.month, summary.balance)
    })
    // for each combined data
    let currentBalance = 0
    const tempBalanceMap = new Map()
    for (let i = combinedData.length - 1; i >= 0; i--) {
      const combined = combinedData[i]
      if (tempHasDataBalancemap.has(combined.month)) {
        currentBalance = tempHasDataBalancemap.get(combined.month)
      }
      tempBalanceMap.set(combined.month, currentBalance)
    }
    if (this.settings.interval === 'year') {
      adminYearlyBalanceMap = tempBalanceMap
    } else {
      adminBalanceMap = tempBalanceMap
    }
    return tempBalanceMap
  }

  initTreasuryBalanceMap (treasuryData, combinedData) {
    if (this.settings.interval !== 'year' && treasuryBalanceMap != null) {
      return treasuryBalanceMap
    }
    if (this.settings.interval === 'year' && treasuryYearlyBalanceMap != null) {
      return treasuryYearlyBalanceMap
    }
    const tempHasDataBalancemap = new Map()
    treasuryData.forEach(summary => {
      tempHasDataBalancemap.set(summary.month, summary.balance)
    })
    // for each combined data
    let currentBalance = 0
    const tempBalanceMap = new Map()
    for (let i = combinedData.length - 1; i >= 0; i--) {
      const combined = combinedData[i]
      if (tempHasDataBalancemap.has(combined.month)) {
        currentBalance = tempHasDataBalancemap.get(combined.month)
      }
      tempBalanceMap.set(combined.month, currentBalance)
    }
    if (this.settings.interval === 'year') {
      treasuryYearlyBalanceMap = tempBalanceMap
    } else {
      treasuryBalanceMap = tempBalanceMap
    }
    return tempBalanceMap
  }

  initCombinedBalanceMap (summaryData) {
    if (this.settings.interval === 'year' && combinedYearlyBalanceMap != null) {
      return combinedYearlyBalanceMap
    }
    if (this.settings.interval !== 'year' && combineBalanceMap != null) {
      return combineBalanceMap
    }
    const tmpCombineBalanceMap = new Map()
    summaryData.forEach(summary => {
      tmpCombineBalanceMap.set(summary.month, summary.balance)
    })

    if (this.settings.interval === 'year') {
      combinedYearlyBalanceMap = tmpCombineBalanceMap
    } else {
      combineBalanceMap = tmpCombineBalanceMap
    }
    return tmpCombineBalanceMap
  }

  getTimeCompare (timStr) {
    const aTimeSplit = timStr.split('-')
    let year = 0
    let month = 0
    if (aTimeSplit.length >= 2) {
      // year
      year = parseInt(aTimeSplit[0])
      // if month < 10
      if (aTimeSplit[1].charAt(0) === '0') {
        month = parseInt(aTimeSplit[1].substring(1, aTimeSplit[1].length))
      } else {
        month = parseInt(aTimeSplit[1])
      }
    } else {
      return 0
    }

    return year * 12 + month
  }

  createTreasuryLegacyTableContent (summary, treasurySummary, legacySummary) {
    const isLegacy = this.settings.ttype === 'legacy'
    const isCombined = !this.settings.ttype || this.settings.ttype === '' || this.settings.ttype === 'combined'
    let thead = '<thead>' +
      '<tr class="text-secondary finance-table-header">' +
      `<th class="va-mid text-center ps-0 month-col cursor-pointer" data-action="click->financereport#sortByCreateDate"><span class="${this.settings.tsort === 'newest' ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype && this.settings.stype !== '' ? 'c-grey-4' : ''} col-sort"></span></th>`
    const usdDisp = this.settings.usd === true || this.settings.usd === 'true'
    thead += '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortByIncoming">Incoming (DCR)</label>' +
      `<span data-action="click->financereport#sortByIncoming" class="${(this.settings.stype === 'incoming' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'incoming' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortByIncoming">Incoming (USD)</label>' +
      `<span data-action="click->financereport#sortByIncoming" class="${(this.settings.stype === 'incoming' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'incoming' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortByOutgoing">Outgoing (DCR)</label>' +
      `<span data-action="click->financereport#sortByOutgoing" class="${(this.settings.stype === 'outgoing' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'outgoing' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortByOutgoing">Outgoing (USD)</label>' +
      `<span data-action="click->financereport#sortByOutgoing" class="${(this.settings.stype === 'outgoing' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'outgoing' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortByNet">Net (DCR)</label>' +
      `<span data-action="click->financereport#sortByNet" class="${(this.settings.stype === 'net' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'net' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortByNet">Net (USD)</label>' +
      `<span data-action="click->financereport#sortByNet" class="${(this.settings.stype === 'net' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'net' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortByBalance">Balance (DCR)</label>' +
      `<span data-action="click->financereport#sortByBalance" class="${(this.settings.stype === 'balance' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'balance' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
      '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortByBalance">Balance (USD)</label>' +
      `<span data-action="click->financereport#sortByBalance" class="${(this.settings.stype === 'balance' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'balance' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>`
    if (!isLegacy) {
      thead += '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortByOutgoingEst">Dev Spent (Est)(DCR)</label>' +
        `<span data-action="click->financereport#sortByOutgoingEst" class="${(this.settings.stype === 'outest' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'outest' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
        '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortByOutgoingEst">Dev Spent (Est)(USD)</label>' +
        `<span data-action="click->financereport#sortByOutgoingEst" class="${(this.settings.stype === 'outest' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'outest' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>` +
        '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortSpentPercent"> Dev Spent (%)</label>' +
        `<span data-action="click->financereport#sortSpentPercent" class="${(this.settings.stype === 'percent' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'percent' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>`
    }
    // Display Usd rate column
    thead += '<th class="va-mid text-right-i ps-0 fs-13i ps-3 pr-3 fw-600 treasury-content-cell"><label class="cursor-pointer" data-action="click->financereport#sortByRate">Rate (USD/DCR)</label>' +
        `<span data-action="click->financereport#sortByRate" class="${(this.settings.stype === 'rate' && this.settings.order === 'desc') ? 'dcricon-arrow-down' : 'dcricon-arrow-up'} ${this.settings.stype !== 'rate' ? 'c-grey-4' : ''} col-sort ms-1"></span></th>`
    thead += '</tr></thead>'
    let tbody = '<tbody>###</tbody>'
    let bodyList = ''
    // create tbody content
    let incomeTotal = 0; let outTotal = 0; let estimateOutTotal = 0
    let incomeUSDTotal = 0; let outUSDTotal = 0; let estimateOutUSDTotal = 0
    let lastBalance = 0; let lastBalanceUSD = 0
    let lastTreasuryItem, lastAdminItem
    let lastMonth = ''
    if (summary.length > 0) {
      lastBalance = summary[0].balance
      lastBalanceUSD = summary[0].balanceUSD
      lastMonth = summary[0].month
    }
    if (treasurySummary.length > 0) {
      lastTreasuryItem = treasurySummary[0]
    }
    if (legacySummary.length > 0) {
      lastAdminItem = legacySummary[0]
    }
    // display balance in top
    if (!isCombined) {
      if (usdDisp) {
        this.treasuryBalanceDisplayTarget.textContent = '$' + humanize.formatToLocalString(lastBalanceUSD, 2, 2)
      } else {
        this.treasuryBalanceDisplayTarget.textContent = humanize.formatToLocalString((lastBalance / 100000000), 2, 2) + ' DCR'
      }
    }
    const balanceMap = this.settings.interval === 'year' ? combinedYearlyBalanceMap : combineBalanceMap
    if (isCombined && lastMonth !== '' && balanceMap.has(lastMonth)) {
      const combined = balanceMap.get(lastMonth)
      let legacy = 0; let tresury = 0
      let legacyUSD = 0; let treasuryUSD = 0
      if (lastAdminItem) {
        legacy = lastAdminItem.balance
        legacyUSD = lastAdminItem.balanceUSD
      }
      if (lastTreasuryItem) {
        tresury = lastTreasuryItem.balance
        treasuryUSD = lastTreasuryItem.balanceUSD
      }
      const lastLegacyRate = 100 * legacy / combined
      const lastTreasuryRate = 100 * tresury / combined
      if (usdDisp) {
        this.treasuryLegacyPercentTarget.textContent = `$${humanize.formatToLocalString((lastBalanceUSD), 2, 2)} `
        this.decentralizedDataTarget.textContent = `$${humanize.formatToLocalString(treasuryUSD, 2, 2)} (${humanize.formatToLocalString(lastTreasuryRate, 2, 2)}%)`
        this.adminDataTarget.textContent = `$${humanize.formatToLocalString(legacyUSD, 2, 2)} (${humanize.formatToLocalString(lastLegacyRate, 2, 2)} %)`
      } else {
        this.treasuryLegacyPercentTarget.textContent = `${humanize.formatToLocalString((lastBalance / 100000000), 2, 2)} DCR`
        this.decentralizedDataTarget.textContent = `${humanize.formatToLocalString((tresury / 100000000), 2, 2)} DCR (${humanize.formatToLocalString(lastTreasuryRate, 2, 2)}%)`
        this.adminDataTarget.textContent = `${humanize.formatToLocalString((legacy / 100000000), 2, 2)} DCR (${humanize.formatToLocalString(lastLegacyRate, 2, 2)} %)`
      }
    }
    const treasuryList = this.sortTreasury(summary)
    const _this = this
    treasuryList.forEach((item) => {
      const timeParam = _this.getFullTimeParam(item.month, '-')
      incomeTotal += item.invalue
      incomeUSDTotal += item.invalueUSD
      outTotal += item.outvalue
      outUSDTotal += item.outvalueUSD
      estimateOutTotal += item.outEstimate
      estimateOutUSDTotal += item.outEstimateUsd

      const netNegative = item.outvalue > item.invalue
      const incomDisplay = item.invalue <= 0 ? '' : humanize.formatToLocalString((item.invalue / 100000000), 2, 2)
      const incomUSDDisplay = item.invalue <= 0 ? '' : humanize.formatToLocalString(item.invalueUSD, 2, 2)
      const outcomeDisplay = item.outvalue <= 0 ? '' : humanize.formatToLocalString((item.outvalue / 100000000), 2, 2)
      const outcomeUSDDisplay = item.outvalue <= 0 ? '' : humanize.formatToLocalString(item.outvalueUSD, 2, 2)
      const differenceDisplay = item.difference <= 0 ? '' : humanize.formatToLocalString((item.difference / 100000000), 2, 2)
      const differenceUSDDisplay = item.difference <= 0 ? '' : humanize.formatToLocalString(item.differenceUSD, 2, 2)
      const balanceDisplay = item.balance <= 0 ? '' : humanize.formatToLocalString((item.balance / 100000000), 2, 2)
      const balanceUSDDisplay = item.balance <= 0 ? '' : humanize.formatToLocalString(item.balanceUSD, 2, 2)
      let incomeHref = ''
      let outcomeHref = ''
      if (isLegacy || _this.settings.ttype === 'current') {
        incomeHref = item.creditLink
        outcomeHref = item.debitLink
      }
      bodyList += '<tr class="odd-even-row">' +
        `<td class="va-mid text-center fs-13i fw-600"><a class="link-hover-underline fs-13i" href="${'/finance-report/detail?type=' + _this.settings.interval + '&time=' + (timeParam === '' ? item.month : timeParam)}">${item.month}</a></td>` +
        `<td class="va-mid text-right-i fs-13i treasury-content-cell">${incomeHref !== '' ? '<a class="link-hover-underline fs-13i" href="' + incomeHref + '">' : ''}${incomDisplay}${incomeHref !== '' ? '</a>' : ''}</td>` +
        `<td class="va-mid text-right-i fs-13i treasury-content-cell">${incomeHref !== '' ? '<a class="link-hover-underline fs-13i" href="' + incomeHref + '">' : ''}${incomUSDDisplay !== '' ? '$' : ''}${incomUSDDisplay}${incomeHref !== '' ? '</a>' : ''}</td>` +
        `<td class="va-mid text-right-i fs-13i treasury-content-cell">${outcomeHref !== '' ? '<a class="link-hover-underline fs-13i" href="' + outcomeHref + '">' : ''}${outcomeDisplay}${outcomeHref !== '' ? '</a>' : ''}</td>` +
        `<td class="va-mid text-right-i fs-13i treasury-content-cell">${outcomeHref !== '' ? '<a class="link-hover-underline fs-13i" href="' + outcomeHref + '">' : ''}${outcomeUSDDisplay !== '' ? '$' : ''}${outcomeUSDDisplay}${outcomeHref !== '' ? '</a>' : ''}</td>` +
        `<td class="va-mid text-right-i fs-13i treasury-content-cell">${netNegative ? '-' : ''}${differenceDisplay}</td>` +
        `<td class="va-mid text-right-i fs-13i treasury-content-cell">${netNegative ? '-' : ''}${differenceUSDDisplay !== '' ? '$' : ''}${differenceUSDDisplay}</td>`
      bodyList += `<td class="va-mid text-right-i fs-13i treasury-content-cell">${balanceDisplay}</td>` +
      `<td class="va-mid text-right-i fs-13i treasury-content-cell">${balanceUSDDisplay !== '' ? '$' : ''}${balanceUSDDisplay}</td>`
      if (!isLegacy) {
        // calculate dev spent percent
        let devSentPercent = 0
        if (item.outvalue > 0) {
          devSentPercent = 100 * 1e8 * item.outEstimate / item.outvalue
        }
        bodyList += `<td class="va-mid text-right-i fs-13i treasury-content-cell">${item.outEstimate === 0.0 ? 'No meta data' : humanize.formatToLocalString(item.outEstimate, 2, 2)}</td>` +
        `<td class="va-mid text-right-i fs-13i treasury-content-cell">${item.outEstimate !== 0.0 ? '$' : ''}${item.outEstimate === 0.0 ? 'No meta data' : humanize.formatToLocalString(item.outEstimateUsd, 2, 2)}</td>`
        bodyList += `<td class="va-mid text-right-i fs-13i treasury-content-cell">${devSentPercent === 0.0 ? 'No dev expenses' : humanize.formatToLocalString(devSentPercent, 2, 2) + '%'}</td>`
      }
      // Display month price of decred
      bodyList += `<td class="va-mid text-right-i fs-13i treasury-content-cell">$${humanize.formatToLocalString(item.monthPrice, 2, 2)}</td>`
      bodyList += '</tr>'
    })
    const totalIncomDisplay = humanize.formatToLocalString((incomeTotal / 100000000), 2, 2)
    const totalIncomUSDDisplay = humanize.formatToLocalString(incomeUSDTotal, 2, 2)
    const totalOutcomeDisplay = humanize.formatToLocalString((outTotal / 100000000), 2, 2)
    const totalOutcomeUSDDisplay = humanize.formatToLocalString(outUSDTotal, 2, 2)
    const totalEstimateOutgoing = humanize.formatToLocalString(estimateOutTotal, 2, 2)
    const totalEstimateOutUSDgoing = humanize.formatToLocalString(estimateOutUSDTotal, 2, 2)
    const lastBalanceDisplay = humanize.formatToLocalString((lastBalance / 100000000), 2, 2)
    const lastBalanceUSDDisplay = humanize.formatToLocalString(lastBalanceUSD, 2, 2)
    const totalBalanceNegative = lastBalance < 0.0
    bodyList += '<tr class="va-mid finance-table-header last-row-header"><td class="text-center fw-600 fs-15i border-right-grey">Total</td>' +
    `<td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell">${totalIncomDisplay}</td>` +
    `<td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell">${incomeUSDTotal > 0 ? '$' : ''}${totalIncomUSDDisplay}</td>` +
    `<td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell">${totalOutcomeDisplay}</td>` +
    `<td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell">${outUSDTotal > 0 ? '$' : ''}${totalOutcomeUSDDisplay}</td>` +
    '<td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell"></td><td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell"></td>'
    bodyList += `<td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell">${totalBalanceNegative ? '-' : ''}${lastBalanceDisplay}</td>` +
    `<td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell">${totalBalanceNegative ? '-' : ''}${usdDisp ? '$' : ''}${lastBalanceUSDDisplay}</td>`
    if (!isLegacy) {
      bodyList += `<td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell">${totalEstimateOutgoing}</td>`
      bodyList += `<td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell">${usdDisp ? '$' : ''}${totalEstimateOutUSDgoing}</td>`
      bodyList += '<td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell"></td>'
    }
    bodyList += '<td class="va-mid text-right-i fw-600 fs-13i treasury-content-cell"></td></tr>'
    tbody = tbody.replace('###', bodyList)
    return thead + tbody
  }

  // Calculate and response
  async calculate () {
    this.pageLoaderTarget.classList.add('loading')
    this.setReportTitle()
    if (this.settings.type === 'treasury') {
      this.searchBoxTarget.classList.add('d-none')
      this.searchBoxTarget.classList.remove('report-search-box')
      this.colorNoteRowTarget.classList.add('d-none')
      this.searchInputTarget.value = ''
      this.settings.search = this.defaultSettings.search
    } else {
      if (this.settings.type !== 'domain') {
        this.settings.chart = this.defaultSettings.chart
      }
      this.searchBoxTarget.classList.remove('d-none')
      this.searchBoxTarget.classList.add('report-search-box')
      if (((this.settings.pgroup === '' || this.settings.pgroup === 'proposals') && this.settings.type === 'summary') || (this.settings.pgroup === 'authors' && this.settings.ptype === 'list')) {
        this.searchBoxTarget.classList.add('ms-3')
      } else {
        this.searchBoxTarget.classList.remove('ms-3')
      }
      this.settings.usd = false
    }

    if (this.settings.type === '' || this.settings.type === 'proposal' || this.settings.type === 'summary') {
      this.proposalSelectTypeTarget.classList.remove('d-none')
      document.getElementById('nameMonthSwitchInput').checked = this.isMonthDisplay
      if ((this.settings.pgroup === 'proposals' && this.settings.type === 'summary') || (this.settings.pgroup === 'authors' && this.settings.ptype === 'list')) {
        this.colorLabelTarget.classList.remove('proposal-note-color')
        this.colorLabelTarget.classList.add('summary-note-color')
        this.reportDescriptionTarget.classList.remove('d-none')
      } else if ((this.settings.pgroup === 'proposals' && (this.settings.type === 'proposal' || this.settings.type === '')) || (this.settings.pgroup === 'authors' && this.settings.ptype !== 'list')) {
        this.colorLabelTarget.classList.remove('summary-note-color')
        this.colorLabelTarget.classList.add('proposal-note-color')
        this.reportDescriptionTarget.classList.add('d-none')
        this.colorDescriptionTarget.textContent = (this.settings.interval === 'year' ? 'Valid payment year (Estimated based on total budget and time period of proposal)' : 'Valid payment month (Estimated based on total budget and time period of proposal)')
      } else {
        this.reportDescriptionTarget.classList.remove('d-none')
      }

      if (this.settings.pgroup === 'domains') {
        this.domainFutureRowTarget.classList.remove('d-none')
      } else {
        this.domainFutureRowTarget.classList.add('d-none')
      }

      // handler for group type
      if (this.settings.pgroup === 'proposals') {
        this.nameMatrixSwitchTarget.classList.remove('d-none')
        this.colorNoteRowTarget.classList.remove('d-none')
        if (this.settings.type === 'summary') {
          this.colorLabelTarget.classList.remove('proposal-note-color')
          this.colorLabelTarget.classList.add('summary-note-color')
          this.colorDescriptionTarget.textContent = 'The proposals are still active'
        } else {
          this.colorLabelTarget.classList.remove('summary-note-color')
          this.colorLabelTarget.classList.add('proposal-note-color')
        }
      } else if (this.settings.pgroup === 'domains') {
        this.nameMatrixSwitchTarget.classList.add('d-none')
        this.colorNoteRowTarget.classList.add('d-none')
      } else if (this.settings.pgroup === 'authors') {
        this.colorNoteRowTarget.classList.remove('d-none')
        this.nameMatrixSwitchTarget.classList.remove('d-none')
        if (this.settings.ptype === 'list') {
          this.colorDescriptionTarget.textContent = 'The authors are still active'
        }
      }
    } else {
      this.nameMatrixSwitchTarget.classList.add('d-none')
      this.proposalSelectTypeTarget.classList.add('d-none')
      this.reportDescriptionTarget.classList.remove('d-none')
      this.domainFutureRowTarget.classList.add('d-none')
    }
    // disabled group button for loading
    this.disabledGroupButton()
    if (!this.settings.search || this.settings.search === '') {
      let haveResponseData
      // if got report. ignore get by api
      if (this.settings.type === 'treasury') {
        if (treasuryResponse !== null) {
          responseData = treasuryResponse
          haveResponseData = true
        }
      } else if (proposalResponse !== null) {
        responseData = proposalResponse
        haveResponseData = true
      }

      if (haveResponseData) {
        if (this.isDomainType()) {
          domainYearData = this.getProposalYearlyData(responseData)
        }
        this.handlerDataForDomainChart(responseData)
        this.createReportTable()
        this.enabledGroupButton()
        this.pageLoaderTarget.classList.remove('loading')
        return
      }
    }

    // const _this = this
    requestCounter++
    const thisRequest = requestCounter

    const url = this.getApiUrlByType()
    let response
    if (hasCache(url)) {
      response = responseCache[url]
    } else {
      // response = await axios.get(url)
      response = await requestJSON(url)
      responseCache[url] = response
      if (thisRequest !== requestCounter) {
        // new request was issued while waiting.
        console.log('Response request different')
      }
    }
    // create table data
    responseData = response
    if (this.isDomainType()) {
      domainYearData = this.getProposalYearlyData(responseData)
    }
    // handler for domain chart
    this.handlerDataForDomainChart(response)
    if (!this.settings.search || this.settings.search === '') {
      if (this.settings.type === 'treasury') {
        treasuryResponse = response
      } else {
        proposalResponse = response
      }
    }
    this.createReportTable()
    this.enabledGroupButton()
    this.pageLoaderTarget.classList.remove('loading')
  }

  isMobile () {
    try { document.createEvent('TouchEvent'); return true } catch (e) { return false }
  }

  handlerDataForCombinedChart (data) {
    if (combinedChartData !== null && combinedChartYearData !== null) {
      return
    }
    if (!data || data.length === 0) {
      combinedChartData = {}
      combinedChartYearData = {}
      return
    }

    combinedChartData = this.getDataForCombinedChart(data, 'month')
    const treasuryYearlyData = this.getTreasuryYearlyData(data)
    combinedChartYearData = this.getDataForCombinedChart(treasuryYearlyData, 'year')
  }

  getDataForCombinedChart (data, type) {
    const timeArr = []
    const spentArr = []
    const receivedArr = []
    const netArr = []
    for (let i = data.length - 1; i >= 0; i--) {
      const item = data[i]
      if (type === 'month') {
        timeArr.push(item.month + '-01T07:00:00Z')
      } else {
        timeArr.push(item.month + '-01-01T07:00:00Z')
      }
      spentArr.push(item.outvalue / 100000000)
      receivedArr.push(item.invalue / 100000000)
      netArr.push((item.invalue - item.outvalue) / 100000000)
    }
    const result = {
      time: timeArr,
      received: receivedArr,
      sent: spentArr,
      net: netArr
    }
    return result
  }

  handlerDataForDomainChart (data) {
    // if data is existed, skip
    if (domainChartData !== null && domainChartYearData !== null) {
      return
    }
    if (!data.report || data.report.length === 0) {
      domainChartData = null
      domainChartYearData = null
      return
    }
    // get monthly data
    domainChartData = this.getDataOfDomainChart(data, 'month')
    if (domainYearData != null) {
      domainChartYearData = this.getDataOfDomainChart(domainYearData, 'year')
    }
  }

  getDataOfDomainChart (data, type) {
    // handler for yearlydata
    const timeArr = []
    const marketingArr = []
    const developmentArr = []
    const virtualNetArr = []
    for (let i = data.report.length - 1; i >= 0; i--) {
      const item = data.report[i]
      if (type === 'month') {
        timeArr.push(item.month.replace('/', '-') + '-01T07:00:00Z')
      } else {
        timeArr.push(item.month + '-01-01T07:00:00Z')
      }
      virtualNetArr.push(0)
      let hasMarketing = false
      let hasDevelopment = false
      item.domainData.forEach((domainData) => {
        if (domainData.domain === 'marketing') {
          marketingArr.push(domainData.expense)
          hasMarketing = true
        } else if (domainData.domain === 'development') {
          developmentArr.push(domainData.expense)
          hasDevelopment = true
        }
      })
      if (!hasMarketing) {
        marketingArr.push(0)
      }
      if (!hasDevelopment) {
        developmentArr.push(0)
      }
    }
    const result = {
      time: timeArr,
      marketing: marketingArr,
      development: developmentArr
    }
    return result
  }

  enabledGroupButton () {
    // enabled group button after loading
    this.intervalTargets.forEach((intervalTarget) => {
      intervalTarget.disabled = false
    })
  }

  disabledGroupButton () {
    // disabled group button after loading
    this.intervalTargets.forEach((intervalTarget) => {
      intervalTarget.disabled = true
    })
  }

  activeProposalSwitch (e) {
    const switchCheck = document.getElementById('activeProposalInput').checked
    this.settings.active = switchCheck
    this.calculate()
  }

  nameMatrixSwitchEvent (e) {
    const switchCheck = document.getElementById('nameMonthSwitchInput').checked
    this.isMonthDisplay = switchCheck
    // if is proposals group type
    if (this.settings.pgroup === 'proposals') {
      this.settings.type = !switchCheck || switchCheck === 'false' ? 'summary' : 'proposal'
    } else {
      this.settings.ptype = !switchCheck || switchCheck === 'false' ? 'list' : 'month'
    }
    this.calculate()
  }

  proposalReportTimeDetail (e) {
    const idArr = e.target.id.split(';')
    if (idArr.length !== 2) {
      return
    }
    window.location.href = '/finance-report/detail?type=' + idArr[0] + '&time=' + idArr[1].replace('/', '_')
  }

  get chartType () {
    if (this.isDomainType()) {
      return 'amountflow'
    }
    return this.optionsTarget.value
  }

  get activeZoomDuration () {
    return this.activeZoomKey ? Zoom.mapValue(this.activeZoomKey) : false
  }

  get activeZoomKey () {
    const activeButtons = this.zoomTarget.getElementsByClassName('btn-selected')
    if (activeButtons.length === 0) return null
    return activeButtons[0].name
  }

  get chartDuration () {
    return this.xRange[1] - this.xRange[0]
  }

  get activeBin () {
    return this.cintervalTarget.getElementsByClassName('btn-selected')[0].name
  }

  get flow () {
    let base10 = 0
    this.flowBoxes.forEach((box) => {
      if (box.checked) base10 += parseInt(box.value)
    })
    return base10
  }
}
