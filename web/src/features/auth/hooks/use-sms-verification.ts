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
import i18next from 'i18next'
import { useState } from 'react'
import { toast } from 'sonner'

import { useCountdown } from '@/hooks/use-countdown'

import { sendSMSVerification } from '../api'
import { SMS_VERIFICATION_COUNTDOWN } from '../constants'

interface UseSMSVerificationOptions {
  turnstileToken?: string
  validateTurnstile?: () => boolean
}

export function useSMSVerification(options?: UseSMSVerificationOptions) {
  const [isSending, setIsSending] = useState(false)
  const {
    secondsLeft,
    isActive,
    start: startCountdown,
  } = useCountdown({ initialSeconds: SMS_VERIFICATION_COUNTDOWN })

  const sendCode = async (phone: string) => {
    if (!/^1\d{10}$/.test(phone.trim())) {
      toast.error(i18next.t('Please enter a valid phone number'))
      return false
    }
    if (options?.validateTurnstile && !options.validateTurnstile()) {
      return false
    }

    setIsSending(true)
    try {
      const res = await sendSMSVerification(
        phone.trim(),
        options?.turnstileToken
      )
      if (res?.success) {
        startCountdown()
        toast.success(i18next.t('Verification SMS sent'))
        return true
      }
      toast.error(res?.message || i18next.t('Failed to send verification SMS'))
      return false
    } catch {
      return false
    } finally {
      setIsSending(false)
    }
  }

  return { isSending, secondsLeft, isActive, sendCode }
}
