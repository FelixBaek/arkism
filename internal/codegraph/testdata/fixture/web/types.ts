export interface Item { id: string }
export type Id = string;

export function load(id: Id): Promise<Item> {
  return fetch(id).then((r) => parse(r));
}

function parse(r: any): Item { return r as Item; }
