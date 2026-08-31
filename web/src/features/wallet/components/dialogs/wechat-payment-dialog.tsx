/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useEffect, useEffectEvent } from 'react'

import { QRCodeSVG } from 'qrcode.react'
import { useTranslation } from 'react-i18next'

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

import { getWeChatPaymentStatus } from '../../api'
import { formatCurrency } from '../../lib'

interface WeChatPaymentDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onPaymentConfirmed: (tradeNo: string) => void
  codeUrl: string
  tradeNo: string
  paymentAmount: number
}

export function WeChatPaymentDialog(props: WeChatPaymentDialogProps) {
  const { t } = useTranslation()
  const notifyPaymentConfirmed = useEffectEvent(props.onPaymentConfirmed)
  const open = props.open
  const tradeNo = props.tradeNo

  useEffect(() => {
    if (!open || !tradeNo) return

    let active = true
    let checking = false
    const checkPaymentStatus = async () => {
      if (checking) return
      checking = true
      try {
        const response = await getWeChatPaymentStatus(tradeNo)
        if (
          active &&
          response.data?.trade_no === tradeNo &&
          response.data.status === 'success'
        ) {
          active = false
          notifyPaymentConfirmed(tradeNo)
        }
      } catch {
        // A later polling attempt can recover from a transient request failure.
      } finally {
        checking = false
      }
    }

    void checkPaymentStatus()
    const intervalId = window.setInterval(checkPaymentStatus, 2000)
    return () => {
      active = false
      window.clearInterval(intervalId)
    }
  }, [open, tradeNo])

  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className='sm:max-w-sm'>
        <DialogHeader className='text-center'>
          <DialogTitle>{t('Pay with WeChat')}</DialogTitle>
          <DialogDescription>
            {t('Scan the QR code with WeChat to complete payment')}
          </DialogDescription>
        </DialogHeader>

        <div className='flex flex-col items-center gap-4 py-2'>
          <div className='rounded-lg border bg-white p-3'>
            <QRCodeSVG value={props.codeUrl} size={220} level='M' />
          </div>
          <div className='text-center'>
            <div className='text-2xl font-semibold'>
              {formatCurrency(props.paymentAmount)}
            </div>
            <div className='text-muted-foreground mt-1 text-xs break-all'>
              {t('Order number')}: {props.tradeNo}
            </div>
          </div>
          <p className='text-muted-foreground text-center text-sm'>
            {t('Your balance will be updated after payment is confirmed')}
          </p>
        </div>
      </DialogContent>
    </Dialog>
  )
}
