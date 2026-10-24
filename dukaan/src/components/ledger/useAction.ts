'use client'

/**
 * A mutation around any async call (POST with a path built from the input, 202 responses,
 * raw downloads…). Toasts the server message on error and invalidates query prefixes on success —
 * the same contract as useSave, for calls useSave cannot express (bahi/lenden reject unknown body fields,
 * so the id cannot ride along in the body).
 */
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useToast } from '@/components/ui'
import { errorMessage } from '@/lib/api'

export function useAction<TOut, TIn = void>(fn: (input: TIn) => Promise<TOut>, opts?: {
  invalidate?: string[]
  onSuccess?: (out: TOut, input: TIn) => void
  successMessage?: string
  silent?: boolean
}) {
  const qc = useQueryClient()
  const toast = useToast()
  return useMutation<TOut, Error, TIn>({
    mutationFn: fn,
    onSuccess: (out, input) => {
      // Path-prefix match, so '/bahi/v1/parties' also refreshes '/bahi/v1/parties/{id}' and its statement.
      for (const key of opts?.invalidate ?? []) void qc.invalidateQueries({ predicate: (q) => typeof q.queryKey[0] === 'string' && q.queryKey[0].startsWith(key) })
      if (opts?.successMessage) toast(opts.successMessage)
      opts?.onSuccess?.(out, input)
    },
    onError: (e) => { if (!opts?.silent) toast(errorMessage(e), 'error') },
  })
}
