// formatting helpers
export function fmt(x) { return String(x); }
export const twice = (x) => fmt(x) + fmt(x);
const pattern = /["'`]+/g; // a regex with quotes must not open a string
export function strip(s) { return s.replace(pattern, ""); }
