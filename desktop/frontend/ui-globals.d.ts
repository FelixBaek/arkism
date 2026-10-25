// Globals the classic UI scripts share through window (type checking only; never shipped).
interface Window {
  [name: `Arkism${string}`]: any;
  __TAURI__?: any;
  mountArkismTriangle: (el: Element, opts?: object) => () => void;
  ask: (message: string, okLabel?: string) => Promise<boolean>;
  askText: (message: string, value?: string) => Promise<string | null>;
  askTyped: (message: string, match: string, okLabel?: string) => Promise<boolean>;
}
declare var ask: Window["ask"];
declare var askText: Window["askText"];
declare var askTyped: Window["askTyped"];
declare var module: { exports: any } | undefined;
