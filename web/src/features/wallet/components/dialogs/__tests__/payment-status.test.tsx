/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'

import { Window } from 'happy-dom'

const bunTestModule = 'bun:test'
const { afterEach, mock, test } = (await import(bunTestModule)) as {
  afterEach: typeof import('node:test').afterEach
  mock: {
    module: (specifier: string, factory: () => object) => void
  }
  test: typeof import('node:test').test
}

const domWindow = new Window()
for (const key of [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'PointerEvent',
  'MouseEvent',
  'FocusEvent',
  'CustomEvent',
  'MutationObserver',
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18n = (await import('i18next')).default
const { I18nextProvider, initReactI18next } = await import('react-i18next')
let paymentStatusResponse: Awaited<
  ReturnType<typeof import('../../../api').getWeChatPaymentStatus>
>
mock.module('../../../api', () => ({
  getWeChatPaymentStatus: async () => paymentStatusResponse,
}))
const { WeChatPaymentDialog } = await import('../wechat-payment-dialog')

await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

let root: ReturnType<typeof createRoot> | null = null

afterEach(async () => {
  if (root) {
    await act(async () => root?.unmount())
    root = null
  }
})

test('notifies the parent when the exact WeChat order is successful', async () => {
  paymentStatusResponse = {
    success: true,
    data: {
      trade_no: 'wechat-paid-order',
      status: 'success',
    },
  }

  let confirmedTradeNo = ''
  const host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)

  await act(async () => {
    root?.render(
      <I18nextProvider i18n={i18n}>
        <WeChatPaymentDialog
          open
          onOpenChange={() => {}}
          onPaymentConfirmed={(tradeNo) => {
            confirmedTradeNo = tradeNo
          }}
          codeUrl='weixin://wxpay/example'
          tradeNo='wechat-paid-order'
          paymentAmount={73}
        />
      </I18nextProvider>
    )
    await Promise.resolve()
  })

  assert.equal(confirmedTradeNo, 'wechat-paid-order')
})