import type { Invoice } from '../shared/invoices.ts'

// Future platform adapters receive this immutable, server-calculated snapshot.
// Manual submission uses exactly the same payload; no supplier endpoint is called yet.
export type InvoicePlatformPayload = ReturnType<typeof invoicePlatformPayload>
export interface InvoicePlatformAdapter {
  submit(payload: InvoicePlatformPayload): Promise<{ reference: string }>
}
export function invoicePlatformPayload(invoice: Invoice) {
  return {
    requestId: invoice.id, title: invoice.title, taxNumber: invoice.tax_number,
    invoiceType: invoice.invoice_type, amountCents: invoice.invoice_cents,
    email: invoice.email, address: invoice.address, phone: invoice.phone,
    bankName: invoice.bank_name, bankAccount: invoice.bank_account, note: invoice.note,
  }
}
