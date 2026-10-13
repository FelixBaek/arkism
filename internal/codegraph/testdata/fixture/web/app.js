import { fmt, twice as tw } from './util.js';
import * as u from './util';

class View {
  render() { return this.title() + tw(1); }
  title() { return fmt('t'); }
}

function main() {
  const v = new View();
  return u.strip(v.render());
}

// a template literal: calls inside ${} still count, text does not
const banner = `fmt(no) ${fmt(1)} and ${main()}`;
export default main;
