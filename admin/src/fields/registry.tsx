import type { ReactNode } from "react";
import type { FieldSpec } from "../api/types";
import { TelCell, TelInput } from "./tel";
import { FinanceCell, FinanceInput } from "./finance";
import { CurrencyCell, CurrencyInput } from "./currency";
import { SlugCell, SlugInput } from "./slug";
import { CountryCell, CountryInput } from "./country";
import { IbanCell, IbanInput } from "./iban";
import { BicCell, BicInput } from "./bic";
import { TaxIdCell, TaxIdInput } from "./tax_id";
import { BarcodeCell, BarcodeInput } from "./barcode";
import { ColorCell, ColorInput } from "./color";
import { StatusCell, StatusInput } from "./status";
import { PriorityCell, PriorityInput } from "./priority";
import { RatingCell, RatingInput } from "./rating";
import { TagsCell, TagsInput } from "./tags";
import { TreePathCell, TreePathInput } from "./tree_path";
import { CoordinatesCell, CoordinatesInput } from "./coordinates";
import { AddressCell, AddressInput } from "./address";
import { BankAccountCell, BankAccountInput } from "./bank_account";
import { QuantityCell, QuantityInput } from "./quantity";
import { DurationCell, DurationInput } from "./duration";
import { LanguageCell, LanguageInput } from "./language";
import { LocaleCell, LocaleInput } from "./locale";
import { CronCell, CronInput } from "./cron";
import { MarkdownCell, MarkdownInput } from "./markdown";
import { QrCodeCell, QrCodeInput } from "./qr_code";

// Field-type registry — dispatches by `field.type` (the runtime string
// from the backend schema endpoint). The 25 domain types listed below
// short-circuit; anything else returns `null` from the renderer-lookup
// fns so the call site can fall back to its existing plain-text /
// generic-input path. The fallback contract matters: this registry is
// strictly additive — non-domain field types must render identically
// to how they did before the registry was introduced.
//
// The runtime `field.type` is wider than the closed FieldSpec.type
// union (the union still tracks only the 15 PB-parity types in v0.8;
// the domain types arrived in v1.4 and the TS shape lags by design —
// nothing in the admin imports them by name). We cast to string and
// switch by value.

type Renderer = (value: unknown) => ReactNode;
type Editor = (value: unknown, onChange: (v: unknown) => void) => ReactNode;

// renderCell returns a ReactNode for a list cell when the field type
// has a domain-specific renderer; returns null otherwise, signalling
// the caller to fall through to its default rendering.
export function renderCell(field: FieldSpec, value: unknown): ReactNode | null {
  const r = cellRenderer(field);
  if (!r) return null;
  return r(value);
}

// renderEditInput returns a ReactNode for an edit input when the
// field type has a domain-specific editor; returns null otherwise.
export function renderEditInput(
  field: FieldSpec,
  value: unknown,
  onChange: (v: unknown) => void,
): ReactNode | null {
  const e = editInputRenderer(field);
  if (!e) return null;
  return e(value, onChange);
}

// hasDomainRenderer is a cheap predicate used by record list code to
// decide whether the inline-edit path (which currently knows how to
// produce a generic <input>) should defer to the per-field renderer
// here. Mirrors the dispatch table below.
export function hasDomainRenderer(field: FieldSpec): boolean {
  return cellRenderer(field) !== null;
}

function cellRenderer(field: FieldSpec): Renderer | null {
  switch (field.type as string) {
    case "tel":
      return (v) => <TelCell value={v} />;
    case "finance":
      return (v) => <FinanceCell value={v} />;
    case "currency":
      return (v) => <CurrencyCell value={v} />;
    case "slug":
      return (v) => <SlugCell value={v} />;
    case "country":
      return (v) => <CountryCell value={v} />;
    case "iban":
      return (v) => <IbanCell value={v} />;
    case "bic":
      return (v) => <BicCell value={v} />;
    case "tax_id":
      return (v) => <TaxIdCell value={v} />;
    case "barcode":
      return (v) => <BarcodeCell value={v} />;
    case "color":
      return (v) => <ColorCell value={v} />;
    case "status":
      return (v) => <StatusCell value={v} />;
    case "priority":
      return (v) => <PriorityCell value={v} />;
    case "rating":
      return (v) => <RatingCell value={v} />;
    case "tags":
      return (v) => <TagsCell value={v} />;
    case "tree_path":
      return (v) => <TreePathCell value={v} />;
    case "coordinates":
      return (v) => <CoordinatesCell value={v} />;
    case "address":
      return (v) => <AddressCell value={v} />;
    case "bank_account":
      return (v) => <BankAccountCell value={v} />;
    case "quantity":
      return (v) => <QuantityCell value={v} />;
    case "duration":
      return (v) => <DurationCell value={v} />;
    case "language":
      return (v) => <LanguageCell value={v} />;
    case "locale":
      return (v) => <LocaleCell value={v} />;
    case "cron":
      return (v) => <CronCell value={v} />;
    case "markdown":
      return (v) => <MarkdownCell value={v} />;
    case "qr_code":
      return (v) => <QrCodeCell value={v} />;
    default:
      return null;
  }
}

function editInputRenderer(field: FieldSpec): Editor | null {
  switch (field.type as string) {
    case "tel":
      return (v, oc) => <TelInput value={v} onChange={oc} />;
    case "finance":
      return (v, oc) => <FinanceInput value={v} onChange={oc} />;
    case "currency":
      return (v, oc) => <CurrencyInput value={v} onChange={oc} />;
    case "slug":
      return (v, oc) => <SlugInput value={v} onChange={oc} />;
    case "country":
      return (v, oc) => <CountryInput value={v} onChange={oc} />;
    case "iban":
      return (v, oc) => <IbanInput value={v} onChange={oc} />;
    case "bic":
      return (v, oc) => <BicInput value={v} onChange={oc} />;
    case "tax_id":
      return (v, oc) => <TaxIdInput field={field} value={v} onChange={oc} />;
    case "barcode":
      return (v, oc) => <BarcodeInput value={v} onChange={oc} />;
    case "color":
      return (v, oc) => <ColorInput value={v} onChange={oc} />;
    case "status":
      return (v, oc) => <StatusInput field={field} value={v} onChange={oc} />;
    case "priority":
      return (v, oc) => <PriorityInput value={v} onChange={oc} />;
    case "rating":
      return (v, oc) => <RatingInput value={v} onChange={oc} />;
    case "tags":
      return (v, oc) => <TagsInput value={v} onChange={oc} />;
    case "tree_path":
      return (v, oc) => <TreePathInput value={v} onChange={oc} />;
    case "coordinates":
      return (v, oc) => <CoordinatesInput value={v} onChange={oc} />;
    case "address":
      return (v, oc) => <AddressInput value={v} onChange={oc} />;
    case "bank_account":
      return (v, oc) => <BankAccountInput field={field} value={v} onChange={oc} />;
    case "quantity":
      return (v, oc) => <QuantityInput field={field} value={v} onChange={oc} />;
    case "duration":
      return (v, oc) => <DurationInput value={v} onChange={oc} />;
    case "language":
      return (v, oc) => <LanguageInput value={v} onChange={oc} />;
    case "locale":
      return (v, oc) => <LocaleInput value={v} onChange={oc} />;
    case "cron":
      return (v, oc) => <CronInput value={v} onChange={oc} />;
    case "markdown":
      return (v, oc) => <MarkdownInput value={v} onChange={oc} />;
    case "qr_code":
      return (v, oc) => <QrCodeInput value={v} onChange={oc} />;
    default:
      return null;
  }
}
