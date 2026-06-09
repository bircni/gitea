import '../../globals.ts';
import '../../../fomantic/build/fomantic.js';
import {initGiteaFomantic} from '../fomantic.ts';

// Regression for a workflow_dispatch "choice" input whose options include boolean/number-like
// values ("true"/"false"/"0"/"1"). jQuery's `.data()` auto-converts these strings to boolean/number,
// which made selecting the "false" option render empty text in the dropdown.
test('dropdown selection keeps boolean-like values as text', () => {
  initGiteaFomantic();
  document.body.innerHTML = `
    <select class="ui selection dropdown" name="success">
      <option value="1" selected>1</option>
      <option value="0">0</option>
      <option value="true">true</option>
      <option value="false">false</option>
    </select>`;
  const select = document.querySelector('select')!;
  $(select).dropdown();
  const dropdown = document.querySelector('.ui.dropdown')!;

  for (const want of ['1', '0', 'true', 'false']) {
    const item = Array.from(dropdown.querySelectorAll<HTMLElement>('.menu > .item')).find((el) => el.getAttribute('data-value') === want)!;
    $(item).trigger('click');
    expect(dropdown.querySelector('.text')!.textContent).toEqual(want);
    expect(select.value).toEqual(want);
  }
});
