import { Controller } from '@hotwired/stimulus'
import globalEventBus from '../services/event_bus_service'
import dompurify from 'dompurify'
import TurboQuery from '../helpers/turbolinks_helper'
import { requestJSON } from '../helpers/http'
import { getDefault } from '../helpers/module_helper'
import { padPoints, sizedBarPlotter } from '../helpers/chart_helper'
import Zoom from '../helpers/zoom_helper'
import { isEmpty } from 'lodash-es'
import humanize from '../helpers/humanize_helper'

let Dygraph // lazy loaded on connect
const maxAddrRows = 160
let ctrl = null
let isSearching = false

function amountTxCountProcessor (chart, d, binSize) {
  const amountData = []
  const txCountData = []
  d.time.map((n, i) => {
    if (chart === 'amount') {
      amountData.push([new Date(n), d.redeemAmount[i], d.refundAmount[i]])
    } else if (chart === 'txcount') {
      txCountData.push([new Date(n), d.redeemCount[i], d.refundCount[i]])
    }
  })
  if (chart === 'amount') {
    padPoints(amountData, binSize)
  } else if (chart === 'txcount') {
    padPoints(txCountData, binSize)
  }
  return {
    amount: amountData,
    txcount: txCountData
  }
}

function formatter (data) {
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
    html += '<br>' + series.dashHTML + l + ': ' + (isNaN(series.y) ? '' : series.y) + '</span>'
  })
  return html
}

function amountFormatter (data) {
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
    html += '<br>' + series.dashHTML + l + ': ' + (isNaN(series.y) ? '' : series.y + ' DCR') + '</span>'
  })
  return html
}

let commonOptions, amountOptions, txCountOptions
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

  amountOptions = {
    labels: ['Date', 'Redeemed Amount', 'Refund Amount'],
    colors: ['#2971ff', '#ff9a29'],
    ylabel: 'Amount (DCR)',
    visibility: [true, true],
    legendFormatter: amountFormatter,
    stackedGraph: true,
    fillGraph: false
  }

  txCountOptions = {
    labels: ['Date', 'Redeemed Count', 'Refund Count'],
    colors: ['#15b16a', '#9c3e76'],
    ylabel: '# of contracts',
    visibility: [true, true],
    legendFormatter: formatter,
    stackedGraph: true,
    fillGraph: false
  }
}

export default class extends Controller {
  static get targets () {
    return ['pagesize', 'txnCount', 'paginator', 'pageplus', 'pageminus', 'listbox', 'table',
      'range', 'pagebuttons', 'listLoader', 'tablePagination', 'paginationheader', 'pair',
      'status', 'topTablePagination', 'fullscreen', 'bigchart', 'littlechart', 'chartbox', 'chartLoader',
      'options', 'zoom', 'interval', 'flow', 'expando', 'noconfirms', 'chart', 'redeemRadio', 'refundRadio',
      'searchBox', 'searchInput', 'searchBtn', 'clearSearchBtn', 'totalAmount', 'totalContracts', 'redeemCount', 'refundCount']
  }

  async connect () {
    ctrl = this
    ctrl.retrievedData = {}
    ctrl.swapsData = null
    ctrl.ajaxing = false
    ctrl.requestedChart = false
    ctrl.zoomCallback = ctrl._zoomCallback.bind(ctrl)
    ctrl.drawCallback = ctrl._drawCallback.bind(ctrl)
    ctrl.lastEnd = 0

    ctrl.bindElements()
    ctrl.bindEvents()
    ctrl.query = new TurboQuery()

    // These two are templates for query parameter sets.
    // When url query parameters are set, these will also be updated.
    ctrl.settings = TurboQuery.nullTemplate(['chart', 'zoom', 'bin', 'flow', 'n', 'start', 'pair', 'status', 'search', 'mode'])
    // Capture template defaults so we can omit them from the URL.
    ctrl.defaultChart = ctrl.optionsTarget.value
    const defaultBinBtn = ctrl.intervalTarget.getElementsByClassName('btn-selected')[0]
    ctrl.defaultBin = defaultBinBtn ? defaultBinBtn.name : null
    let defaultFlow = 0
    ctrl.flowTarget.querySelectorAll('input').forEach((box) => {
      if (box.checked) defaultFlow += parseInt(box.value)
    })
    ctrl.defaultFlow = defaultFlow
    // Get initial view settings from the url
    ctrl.query.update(ctrl.settings)
    ctrl.state = Object.assign({}, ctrl.settings)
    ctrl.settings.pair = ctrl.settings.pair && ctrl.settings.pair !== '' ? ctrl.settings.pair : 'all'
    ctrl.settings.status = ctrl.settings.status && ctrl.settings.status !== '' ? ctrl.settings.status : 'all'
    ctrl.settings.mode = ctrl.settings.mode && ctrl.settings.mode !== '' ? ctrl.settings.mode : 'full'
    document.getElementById('viewListToggle').checked = ctrl.settings.mode !== 'simple'
    this.pairTarget.value = ctrl.settings.pair
    this.statusTarget.value = ctrl.settings.status
    if (!ctrl.settings.start || ctrl.settings.start === '') {
      ctrl.settings.start = 0
    }
    if (!ctrl.settings.n || ctrl.settings.n === '') {
      ctrl.settings.n = 20
    }
    ctrl.paginationParams = {
      offset: ctrl.settings.start,
      count: 0,
      pagesize: ctrl.settings.n
    }

    // init for chart
    ctrl.setChartType()
    if (ctrl.settings.flow) ctrl.setFlowChecks()
    if (ctrl.settings.zoom !== null) {
      ctrl.zoomButtons.forEach((button) => {
        button.classList.remove('btn-selected')
      })
    }
    if (ctrl.settings.bin == null) {
      ctrl.settings.bin = ctrl.getBin()
    }
    if (ctrl.settings.chart == null || !ctrl.validChartType(ctrl.settings.chart)) {
      ctrl.settings.chart = ctrl.chartType
    }

    if (ctrl.settings.search && ctrl.settings.search !== '') {
      this.searchInputTarget.value = ctrl.settings.search
      isSearching = true
      this.searchBtnTarget.classList.add('d-none')
      this.clearSearchBtnTarget.classList.remove('d-none')
    } else {
      this.searchBtnTarget.classList.remove('d-none')
      this.clearSearchBtnTarget.classList.add('d-none')
    }

    Dygraph = await getDefault(
      import(/* webpackChunkName: "dygraphs" */ '../vendor/dygraphs.min.js')
    )

    ctrl.initializeChart()
    ctrl.drawGraph()
    // fetch table data
    await this.fetchTable(ctrl.paginationParams.count, ctrl.paginationParams.offset)
    this.resetPageSizeOptions()
    // get socket atomic summary infos and new updated swap list
    this.processDecredBlock = this._processDecredSummaryInfo.bind(this)
    globalEventBus.on('SUMMARY_RECEIVED', this.processDecredBlock)
    this.processBtcBlock = this._processBtcBlock.bind(this)
    globalEventBus.on('BTC_BLOCK_RECEIVED', this.processBtcBlock)
    this.processLtcBlock = this._processLtcBlock.bind(this)
    globalEventBus.on('LTC_BLOCK_RECEIVED', this.processLtcBlock)
  }

  filteredSettings () {
    const s = Object.assign({}, this.settings)
    if (s.chart === this.defaultChart) s.chart = null
    if (s.bin === this.defaultBin) s.bin = null
    if (s.pair === 'all') s.pair = null
    if (s.status === 'all') s.status = null
    if (s.mode === 'full') s.mode = null
    if (s.start === 0 || s.start === '0' || s.start === '') s.start = null
    if (s.n === 20 || s.n === '20') s.n = null
    if (s.flow != null && parseInt(s.flow) === this.defaultFlow) s.flow = null
    if (s.search === '') s.search = null
    if (s.zoom && this.graph) {
      try {
        if (Zoom.mapKey(s.zoom, this.graph.xAxisExtremes()) === 'all') s.zoom = null
      } catch (e) {}
    }
    return s
  }

  disconnect () {
    if (this.graph !== undefined) {
      this.graph.destroy()
    }
    this.retrievedData = {}
    globalEventBus.off('SUMMARY_RECEIVED', this.processDecredBlock)
    globalEventBus.off('BTC_BLOCK_RECEIVED', this.processBtcBlock)
    globalEventBus.off('LTC_BLOCK_RECEIVED', this.processLtcBlock)
  }

  _processDecredSummaryInfo (data) {
    const summaryData = data.summary_info
    this.updateAtomicSwapsFromSocket(summaryData.GroupSwaps, summaryData.swapsTotalAmount,
      summaryData.swapsTotalContract, summaryData.refundCount)
  }

  _processBtcBlock (blockData) {
    this.updateAtomicSwapsFromSocket(blockData.block.GroupSwaps, blockData.extra.swapsTotalAmount,
      blockData.extra.swapsTotalContract, blockData.extra.refundCount)
  }

  _processLtcBlock (blockData) {
    this.updateAtomicSwapsFromSocket(blockData.block.GroupSwaps, blockData.extra.swapsTotalAmount,
      blockData.extra.swapsTotalContract, blockData.extra.refundCount)
  }

  updateAtomicSwapsFromSocket (swapList, totalAmount, totalContracts, refundCount) {
    if (!swapList || swapList.length === 0) {
      return
    }
    this.totalAmountTarget.innerHTML = humanize.decimalParts(humanize.toAmountFloat(totalAmount), true, 2)
    this.totalContractsTarget.innerHTML = humanize.decimalParts(parseFloat(totalContracts), true, 0)
    this.refundCountTarget.innerHTML = humanize.decimalParts(parseFloat(refundCount), true, 0)
    this.redeemCountTarget.innerHTML = humanize.decimalParts(parseFloat(totalContracts - refundCount), true, 0)
    const insertedGroupTx = []
    if (ctrl.swapsData === null) {
      ctrl.swapsData = swapList.length > ctrl.paginationParams.pagesize ? swapList.slice(0, ctrl.paginationParams.pagesize) : swapList
    } else {
      const newSwaps = []
      for (let i = 0; i < swapList.length; i++) {
        if (i === ctrl.paginationParams.pagesize) {
          break
        }
        if (insertedGroupTx.includes(swapList[i].groupTx)) {
          continue
        }
        newSwaps.push(swapList[i])
        insertedGroupTx.push(swapList[i].groupTx)
      }
      if (newSwaps.length < ctrl.paginationParams.pagesize) {
        for (let i = 0; i < ctrl.swapsData.length; i++) {
          const swap = ctrl.swapsData[i]
          if (newSwaps.length === ctrl.paginationParams.pagesize) {
            break
          }
          if (insertedGroupTx.includes(swap.groupTx)) {
            continue
          }
          newSwaps.push(swap)
          insertedGroupTx.push(swap.groupTx)
        }
      }
      ctrl.swapsData = newSwaps
    }
    this.handlerUpdateTable()
  }

  // Request the initial chart data, grabbing the Dygraph script if necessary.
  initializeChart () {
    createOptions()
    // If no chart data has been requested, e.g. when initially on the
    // list tab, then fetch the initial chart data.
    if (!this.requestedChart) {
      this.fetchGraphData(this.chartType, this.getBin())
    }
  }

  searchInputKeypress (e) {
    if (e.keyCode === 13) {
      this.searchAtomicSwapContract()
    }
  }

  createDataTableView () {
    if (this.swapsData === null) {
      return ''
    }
    let resHtml = `<div class="btable-table-wrap maxh-none mt-2">
      <table class="btable-table w-100 table-responsive-sm">
		  <thead>
		  <tr class="bg-none">
				<th class="text-start fw-bold">Pair/Currency</th>
				<th class="text-center fw-bold">Txid</th>
        <th class="text-start fw-bold">Value</th>
				<th class="text-start fw-bold">Height</th>
				<th class="text-end fw-bold">Time (UTC)</th>
			</tr>
		</thead>
		<tbody class="bgc-white">`
    this.swapsData.forEach((swap) => {
      const hasTargetToken = swap.targetToken !== ''
      resHtml += `<tr class="swap-group-header">
				<td class="text-start" colspan="2">
					<div class="d-flex ai-center">
					${hasTargetToken
? `<div class="p-relative d-flex ai-center pair-icons">
							<img src="/images/dcr-icon-notran.png" width="20" height="20"> 
							<img src="/images/${swap.targetToken}-icon.png" width="20" height="20" class="second-pair">
						  </div>`
: '<img src="/images/synchronize.png" width="20" height="20" class="me-1">'}
						<p class="fw-bold">${hasTargetToken ? 'DCR/' + swap.targetToken.toUpperCase() : 'Verifying'}</p>
						<span class="common-label py-1 px-2 ms-2 ${swap.isRefund ? 'refund-brighter-bg refund-border' : 'success-bg success-border'} fw-400 fs13">${swap.isRefund ? 'Refund' : 'Redemption'}</span>
					</div>
				</td>
				<td class="text-start fw-bold" colspan="2">
          ${humanize.toAmountFloatDisplay(swap.source.totalAmount, -1, 'DCR')}
					${hasTargetToken ? `&nbsp;(${humanize.toAmountFloatDisplay(swap.target.totalAmount, -1, swap.targetToken.toUpperCase())})` : ''}
          ${hasTargetToken ? `<div class="mt-2 fst-italic"><span class="fw-bold">Rate:</span> <span class="fw-400">${humanize.decimalParts(humanize.toAmountFloat(swap.target.totalAmount) / humanize.toAmountFloat(swap.source.totalAmount), true, 7)} ${swap.targetToken.toUpperCase()}/DCR</span></div>` : ''}
				</td>
        <td class="text-end"><span data-type="age" data-time-target="age" data-age="${swap.time}">${humanize.timeDuration(humanize.timeToDuration(swap.time))}</span> ago</td>
			</tr>`
      swap.source.contracts.forEach((contract) => {
        resHtml += `<tr>
				<td class="text-start">
					<div class="d-flex ai-center ms-3 ms-lg-4">
						<div class="d-flex ai-center">
							<img src="/images/dcr-icon-notran.png" width="20" height="20">
							<div class="ms-2"><span class="fw-600">Contract</span></div>
						</div>
					</div>
				</td>
				<td class="text-center">
					<div class="clipboard">${humanize.hashElide(contract.txid, '/tx/' + contract.txid)}</div>
				</td>
        <td class="text-start">
          ${humanize.toAmountFloatDisplay(contract.value, -1, 'DCR')}
				</td>
				<td class="text-start">
					<a href="/decred/block/${contract.height}">${contract.height}</a>
				</td>
				<td class="text-end">
          ${contract.timeDisp}
				</td>
			</tr>`
      })
      swap.source.results.forEach((result) => {
        resHtml += `<tr>
				<td class="text-start">
					<div class="d-flex ai-center ms-3 ms-lg-4">
						<div class="d-flex ai-center">
							<img src="/images/dcr-icon-notran.png" width="20" height="20">
							<div class="ms-2"><span class="fw-600">${swap.isRefund ? 'Refund' : 'Redemption'}</span></div>
						</div>
					</div>
				</td>
				<td class="text-center">
					<div class="clipboard">${humanize.hashElide(result.txid, '/tx/' + result.txid)}</div>
				</td>
        <td class="text-start">
          ${humanize.toAmountFloatDisplay(result.value, -1, 'DCR')}
				</td>
				<td class="text-start">
					<a href="/decred/block/${result.height}">${result.height}</a>
				</td>
				<td class="text-end">
          ${result.timeDisp}
				</td>
			</tr>`
      })
      if (hasTargetToken) {
        swap.target.contracts.forEach((contract) => {
          resHtml += `<tr>
				  <td class="text-start">
					<div class="d-flex ai-center ms-3 ms-lg-4">
						<div class="d-flex ai-center">
							<img src="/images/${swap.targetToken}-icon.png" width="20" height="20">
							<div class="ms-2"><span class="fw-600">Contract</span></div>
						</div>
					</div>
				</td>
				<td class="text-center">
					<div class="clipboard">${humanize.hashElide(contract.txid, '/' + swap.targetToken + '/tx/' + contract.txid)}</div>
				</td>
        <td class="text-start">
					 ${humanize.toAmountFloatDisplay(contract.value, -1, swap.targetToken.toUpperCase())}
				</td>
				<td class="text-start">
					<a href="/${swap.targetToken}/block/${contract.height}">${contract.height}</a>
				</td>
				<td class="text-end">
          ${contract.timeDisp}
				</td>
			</tr>`
        })
        swap.target.results.forEach((result) => {
          resHtml += `<tr>
				<td class="text-start">
					<div class="d-flex ai-center ms-3 ms-lg-4">
						<div class="d-flex ai-center">
							<img src="/images/${swap.targetToken}-icon.png" width="20" height="20">
							<div class="ms-2"><span class="fw-600">${swap.isRefund ? 'Refund' : 'Redemption'}</span></div>
						</div>
					</div>
				</td>
				<td class="text-center">
					<div class="clipboard">${humanize.hashElide(result.txid, '/' + swap.targetToken + '/tx/' + result.txid)}</div>
				</td>
        <td class="text-start">
          ${humanize.toAmountFloatDisplay(result.value, -1, swap.targetToken.toUpperCase())}
				</td>
				<td class="text-start">
					<a href="/${swap.targetToken}/block/${result.height}">${result.height}</a>
				</td>
				<td class="text-end">
          ${result.timeDisp}
				</td>
			</tr>`
        })
      }
    })
    resHtml += '</tbody></table></div>'
    return resHtml
  }

  createGridDataView () {
    if (this.swapsData === null) {
      return ''
    }
    let resHtml = `<div class="btable-table-wrap maxh-none mt-0">
                <table class="btable-table atomic-table w-100 table-responsive-sm">
	              <tbody class="bgc-white">`
    this.swapsData.forEach((swap) => {
      resHtml += `<tr><td>
				<div class="pb-1 border-2-bottom-grey">
				<div class="d-md-flex ai-center fw-600 fs15">
					<div class="d-flex ai-center">`
      const hasTargetToken = swap.targetToken !== ''
      resHtml += `${!hasTargetToken
? '<img src="/images/synchronize.png" width="20" height="20" class="me-1">'
: `<div class="p-relative d-flex ai-center pair-icons">
							<img src="/images/dcr-icon-notran.png" width="20" height="20"> 
							<img src="/images/${swap.targetToken}-icon.png" width="20" height="20" class="second-pair">
						  </div>`}<p>${hasTargetToken ? 'DCR/' + swap.targetToken.toUpperCase() : 'Verifying'}</p></div>`
      resHtml += `<div class="d-flex ai-center ms-0 ms-md-3">Amount:&nbsp;${humanize.toAmountFloatDisplay(swap.source.totalAmount, -1, 'DCR')}
                ${hasTargetToken ? `&nbsp;(${humanize.toAmountFloatDisplay(swap.target.totalAmount, -1, swap.targetToken.toUpperCase())})` : ''}
                <span class="common-label py-1 px-2 ms-2 ${swap.isRefund ? 'refund-brighter-bg refund-border' : 'success-bg success-border'} fw-400 fs13">${swap.isRefund ? 'Refund' : 'Redemption'}</span></div>
                </div>
                <div class="mt-2 fst-italic">
                  ${hasTargetToken ? `<span class="fw-bold">Rate:</span> ${humanize.decimalParts(humanize.toAmountFloat(swap.target.totalAmount) / humanize.toAmountFloat(swap.source.totalAmount), true, 7)} ${swap.targetToken.toUpperCase()}/DCR, ` : ''}
                  <span data-type="age" data-time-target="age" data-age="${swap.time}">${humanize.timeDuration(humanize.timeToDuration(swap.time))}</span> ago
                </div>
                </div><div class="row mt-3">
					      <div class="col-24 col-md-12 mb-3">
						    <div class="d-flex ai-center">
							  <img src="/images/dcr-icon-notran.png" width="20" height="20">
							  <div class="ms-2"><span class="fw-600">Contract</span></div>
						    </div>`
      swap.source.contracts.forEach((contract) => {
        resHtml += `<div class="row mt-1">
							<div class="col-24">
							  <div class="clipboard">${humanize.hashElide(contract.txid, '/tx/' + contract.txid)}</div>
							</div>
							<p class="col-12 ps-2">Block Height: <a href="/decred/block/${contract.height}">${contract.height}</a></p>
							<p class="col-12 ps-2">Value: ${humanize.toAmountFloatDisplay(contract.value, -1, 'DCR')}</p>
							<p class="col-12 ps-2">Fees: ${humanize.toAmountFloatDisplay(contract.fees, -1, 'DCR')}</p>
							<p class="col-12 ps-2"><span class="fw-bold">Created: </span>${contract.timeDisp}</p>
						  </div>`
      })
      resHtml += `</div>
					<div class="col-24 col-md-12 mb-3">
						<div class="d-flex ai-center">
							<img src="/images/dcr-icon-notran.png" width="20" height="20">
							<div class="ms-2"><span class="fw-600">${swap.isRefund ? 'Refund' : 'Redemption'}</span></div>
						</div>`
      swap.source.results.forEach((result) => {
        resHtml += `<div class="row mt-1">
                    <div class="col-24">
                      <div class="clipboard">${humanize.hashElide(result.txid, '/tx/' + result.txid)}</div>
                    </div>
                    <p class="col-12 ps-2">Block Height: <a href="/decred/block/${result.height}">${result.height}</a></p>
                    <p class="col-12 ps-2">Value: ${humanize.toAmountFloatDisplay(result.value, -1, 'DCR')}</p>
                    <p class="col-12 ps-2"><span class="fw-bold">Locked Time: </span>${result.lockTimeDisp}</p>
                    <p class="col-12 ps-2"><span class="fw-bold">${swap.isRefund ? 'Refunded' : 'Redeemed'} At: </span>${result.timeDisp}</p>
                    </div>`
      })
      resHtml += '</div></div>'
      if (hasTargetToken) {
        resHtml += `<div class="row">
					  <div class="col-24 col-md-12 mb-1">
						<div class="d-flex ai-center">
							<img src="/images/${swap.targetToken}-icon.png" width="20" height="20">
							<div class="ms-2"><span class="fw-600">Contracts</span></div>
						</div>`

        swap.target.contracts.forEach((contract) => {
          resHtml += `<div class="row mt-1">
                    <div class="col-24">
                      <div class="clipboard">${humanize.hashElide(contract.txid, '/' + swap.targetToken + '/tx/' + contract.txid)}</div>
                    </div>
                    <p class="col-12 ps-2">Block Height: <a href="/${swap.targetToken}/block/${contract.height}">${contract.height}</a></p>
                    <p class="col-12 ps-2">Value: ${humanize.toAmountFloatDisplay(contract.value, -1, swap.targetToken.toUpperCase())}</p>
                    <p class="col-12 ps-2">Fees: ${humanize.toAmountFloatDisplay(contract.fees, -1, swap.targetToken.toUpperCase())}</p>
                    <p class="col-12 ps-2"><span class="fw-bold">Created: </span>${contract.timeDisp}</p>
                    </div>`
        })
        resHtml += `</div>
					  <div class="col-24 col-md-12 mb-1">
						<div class="d-flex ai-center">
							<img src="/images/${swap.targetToken}-icon.png" width="20" height="20">
							<div class="ms-2"><span class="fw-600">${swap.isRefund ? 'Refund' : 'Redemption'}</span></div>
						</div>`
        swap.target.results.forEach((result) => {
          resHtml += `<div class="row mt-1">
                        <div class="col-24">
                          <div class="clipboard">${humanize.hashElide(result.txid, '/' + swap.targetToken + '/tx/' + result.txid)}</div>
                        </div>
                        <p class="col-12 ps-2">Block Height: <a href="/${swap.targetToken}/block/${result.height}">${result.height}</a></p>
                        <p class="col-12 ps-2">Value: ${humanize.toAmountFloatDisplay(result.value, -1, swap.targetToken.toUpperCase())}</p>
                        <p class="col-12 ps-2"><span class="fw-bold">Locked Time: </span>${result.lockTimeDisp}</p>
                        <p class="col-12 ps-2"><span class="fw-bold">${swap.isRefund ? 'Refunded' : 'Redeemed'} At: </span>${result.timeDisp}</p>
                        </div>`
        })
        resHtml += '</div></div>'
      }
      resHtml += '</td></tr>'
    })
    resHtml += '</tbody></table></div>'
    return resHtml
  }

  searchAtomicSwapContract () {
    // if search key is empty, ignore
    if (!this.searchInputTarget.value || this.searchInputTarget.value === '') {
      this.searchBtnTarget.classList.remove('d-none')
      this.clearSearchBtnTarget.classList.add('d-none')
      if (isSearching) {
        this.settings.search = ''
        isSearching = false
        this.paginationParams.offset = 0
        this.fetchTable(this.pageSize, this.paginationParams.offset)
      }
      return
    }
    this.searchBtnTarget.classList.add('d-none')
    this.clearSearchBtnTarget.classList.remove('d-none')
    this.settings.search = this.searchInputTarget.value
    this.paginationParams.offset = 0
    this.fetchTable(this.pageSize, this.paginationParams.offset)
  }

  async clearSearchState () {
    this.settings.search = ''
    this.searchInputTarget.value = ''
    this.searchBtnTarget.classList.remove('d-none')
    this.clearSearchBtnTarget.classList.add('d-none')
    isSearching = false
    this.paginationParams.offset = 0
    await this.fetchTable(this.pageSize, this.paginationParams.offset)
  }

  async clearSearch () {
    await this.clearSearchState()
    this.resetPageSizeOptions()
  }

  onTypeChange (e) {
    if (!e.target.value || e.target.value === '') {
      return
    }
    this.searchBtnTarget.classList.remove('d-none')
    this.clearSearchBtnTarget.classList.add('d-none')
  }

  drawGraph () {
    const settings = ctrl.settings
    ctrl.noconfirmsTarget.classList.add('d-hide')
    ctrl.chartTarget.classList.remove('d-hide')
    // Check for invalid view parameters
    if (!ctrl.validChartType(settings.chart) || !ctrl.validGraphInterval()) return
    // update flow radio button color
    this.redeemRadioTarget.classList.add(settings.chart === 'amount' ? 'redeem-amount' : 'redeem-txcount')
    this.redeemRadioTarget.classList.remove(settings.chart === 'amount' ? 'redeem-txcount' : 'redeem-amount')
    this.refundRadioTarget.classList.add(settings.chart === 'amount' ? 'refund-amount' : 'refund-txcount')
    this.refundRadioTarget.classList.remove(settings.chart === 'amount' ? 'refund-txcount' : 'refund-amount')

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

  changeViewMode () {
    ctrl.settings.mode = ctrl.settings.mode === 'full' ? 'simple' : 'full'
    document.getElementById('viewListToggle').checked = ctrl.settings.mode === 'full'
    ctrl.query.replace(ctrl.filteredSettings())
    ctrl.tableTarget.innerHTML = ctrl.settings.mode === 'full' ? this.createGridDataView() : this.createDataTableView()
  }

  async fetchGraphData (chart, bin) {
    const cacheKey = chart + '-' + bin
    if (ctrl.ajaxing === cacheKey) {
      return
    }
    ctrl.requestedChart = cacheKey
    ctrl.ajaxing = cacheKey

    ctrl.chartLoaderTarget.classList.add('loading')

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
    const url = `/api/atomic-swaps/${chart}/${bin}`
    const graphDataResponse = await requestJSON(url)
    ctrl.processData(chart, bin, graphDataResponse)
    ctrl.ajaxing = false
    ctrl.chartLoaderTarget.classList.remove('loading')
  }

  popChartCache (chart, bin) {
    const cacheKey = chart + '-' + bin
    const binSize = Zoom.mapValue(bin)
    if (!ctrl.retrievedData[cacheKey] ||
      ctrl.requestedChart !== cacheKey
    ) {
      return
    }
    const data = ctrl.retrievedData[cacheKey]
    let options = null
    switch (chart) {
      case 'amount':
        options = amountOptions
        options.plotter = sizedBarPlotter(binSize)
        break
      case 'txcount':
        options = txCountOptions
        options.plotter = sizedBarPlotter(binSize)
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
    ctrl.updateFlow()
    ctrl.chartLoaderTarget.classList.remove('loading')
    ctrl.xRange = ctrl.graph.xAxisExtremes()
    ctrl.validateZoom(binSize)
  }

  processData (chart, bin, data) {
    if (isEmpty(data)) {
      ctrl.noDataAvailable()
      return
    }
    const binSize = Zoom.mapValue(bin)
    const processed = amountTxCountProcessor(chart, data, binSize)
    ctrl.retrievedData[chart + '-' + bin] = chart === 'amount' ? processed.amount : processed.txcount
    setTimeout(() => {
      ctrl.popChartCache(chart, bin)
    }, 0)
  }

  noDataAvailable () {
    this.noconfirmsTarget.classList.remove('d-hide')
    this.chartTarget.classList.add('d-hide')
    this.chartLoaderTarget.classList.remove('loading')
  }

  updateFlow () {
    const bitmap = ctrl.flow
    if (bitmap === 0) {
      // If all boxes are unchecked, just leave the last view
      // in place to prevent chart errors with zero visible datasets
      return
    }
    ctrl.settings.flow = bitmap
    ctrl.setGraphQuery()
    // Set the graph dataset visibility based on the bitmap
    // Dygraph dataset indices: 0 received, 1 sent, 2 & 3 net
    const visibility = {}
    visibility[0] = bitmap & 1
    visibility[1] = bitmap & 2
    Object.keys(visibility).forEach((idx) => {
      ctrl.graph.setVisibility(idx, visibility[idx])
    })
  }

  validateZoom (binSize) {
    ctrl.setButtonVisibility()
    const zoom = Zoom.validate(ctrl.activeZoomKey || ctrl.settings.zoom, ctrl.xRange, binSize)
    ctrl.setZoom(zoom.start, zoom.end)
    ctrl.graph.updateOptions({
      zoomCallback: ctrl.zoomCallback,
      drawCallback: ctrl.drawCallback
    })
  }

  changeGraph (e) {
    this.settings.chart = this.chartType
    this.setGraphQuery()
    this.drawGraph()
  }

  changeBin (e) {
    const target = e.srcElement || e.target
    if (target.nodeName !== 'BUTTON') return
    ctrl.settings.bin = target.name
    ctrl.setIntervalButton(target.name)
    this.setGraphQuery()
    this.drawGraph()
  }

  setZoom (start, end) {
    if (ctrl.graph === undefined) {
      return
    }
    ctrl.chartLoaderTarget.classList.add('loading')
    ctrl.graph.updateOptions({
      dateWindow: [start, end]
    })
    ctrl.settings.zoom = Zoom.encode(start, end)
    ctrl.lastEnd = end
    ctrl.query.replace(ctrl.filteredSettings())
    ctrl.chartLoaderTarget.classList.remove('loading')
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

  setGraphQuery () {
    this.query.replace(this.filteredSettings())
  }

  createGraph (processedData, otherOptions) {
    return new Dygraph(
      this.chartTarget,
      processedData,
      { ...commonOptions, ...otherOptions }
    )
  }

  getBin () {
    let bin = ctrl.query.get('bin')
    if (!ctrl.setIntervalButton(bin)) {
      bin = ctrl.activeBin
    }
    return bin
  }

  setIntervalButton (interval) {
    const button = ctrl.validGraphInterval(interval)
    if (!button) return false
    ctrl.binputs.forEach((button) => {
      button.classList.remove('btn-selected')
    })
    button.classList.add('btn-selected')
  }

  setViewButton (view) {
    this.viewTargets.forEach((button) => {
      if (button.name === view) {
        button.classList.add('btn-active')
      } else {
        button.classList.remove('btn-active')
      }
    })
  }

  validGraphInterval (interval) {
    const bin = interval || this.settings.bin || this.activeBin
    let b = false
    this.binputs.forEach((button) => {
      if (button.name === bin) b = button
    })
    return b
  }

  setChartType () {
    const chart = ctrl.settings.chart
    if (this.validChartType(chart)) {
      this.optionsTarget.value = chart
    }
  }

  validChartType (chart) {
    return this.optionsTarget.namedItem(chart) || false
  }

  setFlowChecks () {
    const bitmap = this.settings.flow
    this.flowBoxes.forEach((box) => {
      box.checked = bitmap & parseInt(box.value)
    })
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
    const duration = ctrl.activeZoomDuration

    const end = ctrl.xRange[1]
    const start = duration === 0 ? ctrl.xRange[0] : end - duration
    ctrl.setZoom(start, end)
  }

  _zoomCallback (start, end) {
    ctrl.zoomButtons.forEach((button) => {
      button.classList.remove('btn-selected')
    })
    ctrl.settings.zoom = Zoom.encode(start, end)
    ctrl.query.replace(ctrl.filteredSettings())
    ctrl.setSelectedZoom(Zoom.mapKey(ctrl.settings.zoom, ctrl.graph.xAxisExtremes()))
  }

  _drawCallback (graph, first) {
    if (first) return
    const [start, end] = ctrl.graph.xAxisRange()
    if (start === end) return
    if (end === this.lastEnd) return // Only handle slide event.
    this.lastEnd = end
    ctrl.settings.zoom = Zoom.encode(start, end)
    ctrl.query.replace(ctrl.filteredSettings())
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

  bindElements () {
    this.flowBoxes = this.flowTarget.querySelectorAll('input')
    this.pageSizeOptions = this.hasPagesizeTarget ? this.pagesizeTarget.querySelectorAll('option') : []
    this.zoomButtons = this.zoomTarget.querySelectorAll('button')
    this.binputs = this.intervalTarget.querySelectorAll('button')
  }

  bindEvents () {
    ctrl.paginatorTargets.forEach((link) => {
      link.addEventListener('click', (e) => {
        e.preventDefault()
      })
    })
  }

  changePair (e) {
    ctrl.settings.pair = (!e.target.value || e.target.value === '') ? 'all' : e.target.value
    this.clearSearchState()
  }

  changeStatus (e) {
    ctrl.settings.status = (!e.target.value || e.target.value === '') ? 'all' : e.target.value
    this.clearSearchState()
  }

  makeTableUrl (count, offset) {
    return `/atomicswaps-table?n=${count}&start=${offset}${ctrl.settings.pair && ctrl.settings.pair !== '' ? '&pair=' + ctrl.settings.pair : ''}${ctrl.settings.status && ctrl.settings.status !== '' ? '&status=' + ctrl.settings.status : ''}
      ${ctrl.settings.search && ctrl.settings.search !== '' ? '&search=' + ctrl.settings.search : ''}${ctrl.settings.mode && ctrl.settings.mode !== '' ? '&mode=' + ctrl.settings.mode : ''}`
  }

  changePageSize () {
    this.fetchTable(this.pageSize, this.paginationParams.offset)
  }

  nextPage () {
    this.toPage(1)
  }

  prevPage () {
    this.toPage(-1)
  }

  pageNumberLink (e) {
    e.preventDefault()
    const url = e.target.href
    const parser = new URL(url)
    const start = parser.searchParams.get('start')
    const pagesize = parser.searchParams.get('n')
    this.fetchTable(pagesize, start)
  }

  toPage (direction) {
    const params = ctrl.paginationParams
    const count = ctrl.pageSize
    let requestedOffset = params.offset + count * direction
    if (requestedOffset >= params.count) return
    if (requestedOffset < 0) requestedOffset = 0
    ctrl.fetchTable(count, requestedOffset)
  }

  async fetchTable (count, offset) {
    ctrl.listLoaderTarget.classList.add('loading')
    const requestCount = count > 20 ? count : 20
    const tableResponse = await requestJSON(ctrl.makeTableUrl(requestCount, offset))
    this.swapsData = tableResponse.swaps_list
    ctrl.tableTarget.innerHTML = dompurify.sanitize(tableResponse.html)
    const settings = ctrl.settings
    settings.n = requestCount
    settings.start = offset
    ctrl.paginationParams.count = tableResponse.tx_count
    ctrl.query.replace(ctrl.filteredSettings())
    ctrl.paginationParams.offset = offset
    ctrl.paginationParams.pagesize = requestCount
    ctrl.paginationParams.currentcount = Number(tableResponse.current_count)
    ctrl.setPageability()
    ctrl.tablePaginationParams = tableResponse.pages
    ctrl.setTablePaginationLinks()
    ctrl.listLoaderTarget.classList.remove('loading')
  }

  handlerUpdateTable () {
    if (ctrl.swapsData === null) {
      return
    }
    ctrl.listLoaderTarget.classList.add('loading')
    // ctrl.paginationParams.count = txCount
    ctrl.paginationParams.currentcount = ctrl.swapsData.length
    ctrl.setPageability()
    ctrl.tableTarget.innerHTML = ctrl.settings.mode === 'full' ? this.createGridDataView() : this.createDataTableView()
    ctrl.listLoaderTarget.classList.remove('loading')
  }

  // reset page size selector
  resetPageSizeOptions () {
    const params = ctrl.paginationParams
    const rowMax = params.count
    const dispCount = params.currentcount
    let pageSizeOptions = ''
    if (rowMax >= 20) {
      this.pagesizeTarget.classList.remove('disabled')
      this.pagesizeTarget.disabled = false
    } else {
      this.pagesizeTarget.classList.add('disabled')
      this.pagesizeTarget.disabled = true
    }
    pageSizeOptions += `<option ${dispCount === 20 ? 'selected' : ''} value="20" ${rowMax < 20 ? 'disabled' : ''}>20</option>`
    pageSizeOptions += `<option ${dispCount === 40 ? 'selected' : ''} value="40" ${rowMax < 40 ? 'disabled' : ''}>40</option>`
    pageSizeOptions += `<option ${dispCount === 80 ? 'selected' : ''} value="80" ${rowMax < 80 ? 'disabled' : ''}>80</option>`
    if (rowMax <= 160) {
      pageSizeOptions += `<option ${dispCount === rowMax ? 'selected' : ''} value="${rowMax}" ${rowMax <= 160 ? 'disabled' : ''}>${rowMax}</option>`
    } else {
      pageSizeOptions += `<option ${dispCount >= 160 ? 'selected' : ''} value="160">160</option>`
    }
    this.pagesizeTarget.innerHTML = pageSizeOptions
    this.pageSizeOptions = this.hasPagesizeTarget ? this.pagesizeTarget.querySelectorAll('option') : []
    const settings = ctrl.settings
    settings.n = this.pageSize
    ctrl.query.replace(ctrl.filteredSettings())
  }

  setPageability () {
    const params = ctrl.paginationParams
    const rowMax = params.count
    const count = ctrl.pageSize
    if (ctrl.paginationParams.count === 0) {
      ctrl.paginationheaderTarget.classList.add('d-hide')
    } else {
      ctrl.paginationheaderTarget.classList.remove('d-hide')
    }
    if (rowMax > count) {
      ctrl.pagebuttonsTarget.classList.remove('d-hide')
    } else {
      ctrl.pagebuttonsTarget.classList.add('d-hide')
    }
    const setAbility = (el, state) => {
      if (state) {
        el.classList.remove('disabled')
      } else {
        el.classList.add('disabled')
      }
    }
    setAbility(ctrl.pageplusTarget, params.offset + count < rowMax)
    setAbility(ctrl.pageminusTarget, params.offset - count >= 0)
    ctrl.pageSizeOptions.forEach((option) => {
      if (option.value > 100) {
        if (rowMax > 100) {
          option.disabled = false
          option.text = option.value = Math.min(rowMax, maxAddrRows)
        } else {
          option.disabled = true
          option.text = option.value = maxAddrRows
        }
      } else {
        option.disabled = rowMax <= option.value
      }
    })
    setAbility(ctrl.pagesizeTarget, rowMax > 20)
    const suffix = rowMax > 1 ? 's' : ''
    let rangeEnd = Number(params.offset) + Number(count)
    if (rangeEnd > rowMax) rangeEnd = rowMax
    ctrl.rangeTarget.innerHTML = 'showing ' + (Number(params.offset) + 1).toLocaleString() + ' &ndash; ' +
      rangeEnd.toLocaleString() + ' of ' + rowMax.toLocaleString() + ' contract' + suffix
  }

  setTablePaginationLinks () {
    const tablePagesLink = ctrl.tablePaginationParams
    if (tablePagesLink.length === 0) {
      ctrl.tablePaginationTarget.classList.add('d-hide')
      ctrl.topTablePaginationTarget.classList.add('d-hide')
      return
    }
    ctrl.tablePaginationTarget.classList.remove('d-hide')
    ctrl.topTablePaginationTarget.classList.remove('d-hide')
    const txCount = parseInt(ctrl.paginationParams.count)
    const offset = parseInt(ctrl.paginationParams.offset)
    const pageSize = parseInt(ctrl.paginationParams.pagesize)
    let links = ''
    if (typeof offset !== 'undefined' && offset > 0) {
      links = `<a href="/atomic-swaps?start=${offset - pageSize}&n=${pageSize}&pair=${ctrl.settings.pair}&status=${ctrl.settings.status}${ctrl.settings.search && ctrl.settings.search !== '' ? '&search=' + ctrl.settings.search : ''}${ctrl.settings.mode && ctrl.settings.mode !== '' ? '&mode=' + ctrl.settings.mode : ''}" ` +
        'class="d-inline-block dcricon-arrow-left pagination-number pagination-narrow m-1 fz20" data-action="click->atomicswaps#pageNumberLink"></a>' + '\n'
    }

    links += tablePagesLink.map(d => {
      if (!d.link) return `<span>${d.str}</span>`
      return `<a href="${d.link}" class="fs18 pager pagination-number${d.active ? ' active' : ''}" data-action="click->atomicswaps#pageNumberLink">${d.str}</a>`
    }).join('\n')

    if ((txCount - offset) > pageSize) {
      links += '\n' + `<a href="/atomic-swaps?start=${(offset + pageSize)}&n=${pageSize}&pair=${ctrl.settings.pair}&status=${ctrl.settings.status}${ctrl.settings.search && ctrl.settings.search !== '' ? '&search=' + ctrl.settings.search : ''}${ctrl.settings.mode && ctrl.settings.mode !== '' ? '&mode=' + ctrl.settings.mode : ''}" ` +
        'class="d-inline-block dcricon-arrow-right pagination-number pagination-narrow m-1 fs20" data-action="click->atomicswaps#pageNumberLink"></a>'
    }
    const paginationHTML = dompurify.sanitize(links)
    ctrl.tablePaginationTarget.innerHTML = paginationHTML
    ctrl.topTablePaginationTarget.innerHTML = paginationHTML
  }

  get pageSize () {
    const selected = this.pagesizeTarget.selectedOptions
    return selected.length ? parseInt(selected[0].value) : 20
  }

  get chartType () {
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
    return this.intervalTarget.getElementsByClassName('btn-selected')[0].name
  }

  get flow () {
    let base10 = 0
    this.flowBoxes.forEach((box) => {
      if (box.checked) base10 += parseInt(box.value)
    })
    return base10
  }
}
