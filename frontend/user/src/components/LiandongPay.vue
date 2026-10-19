<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'

type Bootstrap = {
  gateway_base?: string
  goods_key?: string
  channel_id?: string
  contact?: string
  query_password?: string
  attach_token?: string
  order_no?: string
  amount_fen?: number
  expires_at?: string
}

const props = defineProps<{
  bootstrap: Bootstrap
  clientGatewayUrl: string
}>()

const emit = defineEmits<{
  paid: []
  review: []
  error: [message: string]
}>()

const { t } = useI18n()

const qrImageUrl = ref('')
const statusText = ref('')
const failed = ref(false)
const running = ref(false)
let stopped = false

const gatewayBase = computed(() => String(props.bootstrap?.gateway_base || '').replace(/\/+$/, '') + '/')
const apiBase = computed(() => String(props.clientGatewayUrl || '').replace(/\/+$/, ''))
const orderNo = computed(() => String(props.bootstrap?.order_no || '').trim())
const storageKey = computed(() => `liandong_trade_no:${orderNo.value}`)

const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms))

const postJSON = async (url: string, body: unknown) => {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body || {}),
  })
  let payload: any = null
  try {
    payload = await res.json()
  } catch {
    payload = null
  }
  if (!res.ok || !payload) {
    throw new Error(payload?.error || `HTTP ${res.status}`)
  }
  return payload
}

const createOnPlatform = async (): Promise<string> => {
  const b = props.bootstrap
  const res = await fetch(gatewayBase.value + 'shopApi/Pay/order', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      goods_key: b.goods_key,
      quantity: 1,
      channel_id: b.channel_id,
      contact: b.contact,
      query_password: b.query_password,
      coupon_code: '',
    }),
  })
  let payload: any = null
  try {
    payload = await res.json()
  } catch {
    payload = null
  }
  if (!res.ok || !payload || payload.code !== 1 || !payload.data?.trade_no) {
    throw new Error(payload?.msg || `HTTP ${res.status}`)
  }
  return String(payload.data.trade_no)
}

const attachTradeNo = async (tradeNo: string): Promise<string> => {
  const payload = await postJSON(apiBase.value + '/api/order/attach', {
    order_no: orderNo.value,
    trade_no: tradeNo,
    attach_token: props.bootstrap?.attach_token,
  })
  // 服务端已有绑定交易号时以服务端为准，避免刷新页面后绑定到新的平台订单。
  const bound = String(payload?.order?.trade_no || '').trim()
  return bound || tradeNo
}

type BoundOrder = { tradeNo: string; status: string; verifyState: string }

// 先向 faka2api 查询本地订单是否已绑定交易号；已绑定则直接复用，
// 避免刷新页面/清缓存后在发卡平台重复下单。
const loadBoundOrder = async (): Promise<BoundOrder> => {
  try {
    const payload = await postJSON(apiBase.value + '/api/order/info', {
      order_no: orderNo.value,
      attach_token: props.bootstrap?.attach_token,
    })
    return {
      tradeNo: String(payload?.order?.trade_no || '').trim(),
      status: String(payload?.order?.status || '').trim(),
      verifyState: String(payload?.order?.verify_state || '').trim(),
    }
  } catch {
    return { tradeNo: '', status: '', verifyState: '' }
  }
}

const extractQR = (html: string): string => {
  const match = html.match(/generateQrcode\.html\?str=([^"'\s<>]+)/)
  if (!match) {
    const bare = html.match(/weixin:\/\/wxpay\/bizpayurl[^"'<\s\\]+/)
    return bare ? bare[0] : ''
  }
  let value: string = match[1] ?? ''
  for (let round = 0; round < 3; round += 1) {
    let decoded = value
    try {
      decoded = decodeURIComponent(value)
    } catch {
      break
    }
    if (decoded === value) break
    value = decoded
    if (/^(weixin:|https?:)/.test(value)) return value
  }
  return /^weixin:\/\//.test(value) ? value : ''
}

const fetchQR = async (tradeNo: string): Promise<string> => {
  const res = await fetch(
    gatewayBase.value + 'payApi/WeixinNative/pay.html?trade_no=' + encodeURIComponent(tradeNo),
  )
  const html = await res.text()
  const qr = extractQR(html)
  if (!qr) {
    throw new Error(t('payment.liandongQrFailed'))
  }
  return qr
}

type PlatformStatus = 'paid' | 'waiting' | 'closed' | 'unknown'

const pollStatus = async (tradeNo: string): Promise<PlatformStatus> => {
  try {
    const res = await fetch(gatewayBase.value + 'payApi/common/checkOrderStatus.html', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: 'trade_no=' + encodeURIComponent(tradeNo),
    })
    const payload: any = await res.json()
    if (payload?.code === 1) return 'paid'
    if (payload?.code === 0) {
      const msg = String(payload?.msg || '')
      if (/不存在|已关闭|已超时|已失效|已取消/.test(msg)) return 'closed'
      return 'waiting'
    }
    return 'unknown'
  } catch {
    return 'unknown'
  }
}

const renderQR = async (content: string) => {
  if (!content) {
    qrImageUrl.value = ''
    return
  }
  try {
    qrImageUrl.value = await QRCode.toDataURL(content, {
      width: 280,
      margin: 1,
      errorCorrectionLevel: 'M',
    })
  } catch {
    qrImageUrl.value = ''
  }
}

const fail = (message: string) => {
  failed.value = true
  statusText.value = message
  emit('error', message)
}

const run = async () => {
  if (running.value || stopped) return
  if (!orderNo.value || !props.bootstrap?.attach_token || !gatewayBase.value || !apiBase.value) {
    fail(t('payment.liandongConfigMissing'))
    return
  }
  running.value = true
  failed.value = false
  try {
    const boundOrder = await loadBoundOrder()
    if (boundOrder.status === 'paid') {
      // 服务端已确认入账（回调可能已到达），直接刷新到账状态。
      statusText.value = t('payment.liandongPaid')
      emit('paid')
      return
    }
    let tradeNo = boundOrder.tradeNo
    if (!tradeNo) {
      try {
        tradeNo = String(window.localStorage.getItem(storageKey.value) || '').trim()
      } catch {
        tradeNo = ''
      }
    }
    if (!tradeNo) {
      statusText.value = t('payment.liandongCreating')
      tradeNo = await createOnPlatform()
      try {
        window.localStorage.setItem(storageKey.value, tradeNo)
      } catch {
        // localStorage 不可用时仅影响刷新恢复，不阻断支付。
      }
    }
    statusText.value = t('payment.liandongAttaching')
    tradeNo = await attachTradeNo(tradeNo)
    try {
      window.localStorage.setItem(storageKey.value, tradeNo)
    } catch {
      // ignore
    }

    statusText.value = t('payment.liandongLoadingQr')
    const qr = await fetchQR(tradeNo)
    await renderQR(qr)
    if (stopped) return
    statusText.value = t('payment.liandongScanTip')

    const deadline = Date.now() + 15 * 60 * 1000
    let status: PlatformStatus = 'waiting'
    while (!stopped && Date.now() < deadline) {
      await sleep(2500)
      if (stopped) return
      status = await pollStatus(tradeNo)
      if (status === 'paid' || status === 'closed') break
    }
    if (stopped) return
    if (status === 'closed') {
      fail(t('payment.liandongOrderClosed'))
      return
    }
    if (status !== 'paid') {
      fail(t('payment.liandongPollTimeout'))
      return
    }

    statusText.value = t('payment.liandongConfirming')
    const confirmed = await postJSON(apiBase.value + '/api/order/confirm', {
      order_no: orderNo.value,
      attach_token: props.bootstrap?.attach_token,
    })
    const verifyState = String(confirmed?.order?.verify_state || '')
    try {
      window.localStorage.removeItem(storageKey.value)
    } catch {
      // ignore
    }
    if (verifyState === 'review') {
      statusText.value = t('payment.liandongReview')
      emit('review')
      return
    }
    statusText.value = t('payment.liandongPaid')
    emit('paid')
  } catch (err: any) {
    if (!stopped) {
      fail(err?.message || t('payment.liandongFlowFailed'))
    }
  } finally {
    running.value = false
  }
}

onMounted(() => {
  void run()
})

onBeforeUnmount(() => {
  stopped = true
})
</script>

<template>
  <div class="flex flex-col items-center gap-3">
    <div
      v-if="qrImageUrl"
      class="aspect-square w-full max-w-[240px] overflow-hidden rounded-xl bg-white p-2"
    >
      <img :src="qrImageUrl" :alt="t('payment.liandongQrAlt')" class="h-full w-full object-contain" />
    </div>
    <div class="text-center text-xs" :class="failed ? 'text-destructive' : 'text-muted-foreground'">
      {{ statusText || t('payment.liandongLoadingQr') }}
    </div>
  </div>
</template>
