import {showInfoToast, showWarningToast, showErrorToast} from './toast.ts';
import type {Toast} from './toast.ts';
import {registerGlobalInitFunc} from './observer.ts';
import {fomanticQuery} from './fomantic/base.ts';
import {createElementFromHTML} from '../utils/dom.ts';
import {html} from '../utils/html.ts';

type LevelMap = Record<string, (message: string) => Toast | null>;

function initDevtestPage() {
  const iconSearch = document.querySelector<HTMLInputElement>('#icon-search');
  const iconSizeToggle = document.querySelector<HTMLInputElement>('#icon-size-toggle');
  if (iconSearch && iconSizeToggle) {
    iconSearch.addEventListener('input', () => {
      const query = iconSearch.value.toLowerCase();
      for (const card of document.querySelectorAll<HTMLElement>('.icon-card')) {
        card.style.display = card.getAttribute('data-name')!.includes(query) ? '' : 'none';
      }
    });

    iconSizeToggle.addEventListener('change', () => {
      const size = iconSizeToggle.checked ? '24' : '16';
      for (const icon of document.querySelectorAll<SVGElement>('.icon-card svg')) {
        icon.setAttribute('width', size);
        icon.setAttribute('height', size);
      }
    });
  }

  const toastButtons = document.querySelectorAll('.toast-test-button');
  if (toastButtons.length) {
    const levelMap: LevelMap = {info: showInfoToast, warning: showWarningToast, error: showErrorToast};
    for (const el of toastButtons) {
      el.addEventListener('click', () => {
        const level = el.getAttribute('data-toast-level')!;
        const message = el.getAttribute('data-toast-message')!;
        levelMap[level](message);
      });
    }
  }

  const modalButtons = document.querySelector('.modal-buttons');
  if (modalButtons) {
    for (const el of document.querySelectorAll('.ui.modal:not([data-skip-button])')) {
      const btn = createElementFromHTML(html`<button class="ui button">${el.id}</button`);
      btn.addEventListener('click', () => fomanticQuery(el).modal('show'));
      modalButtons.append(btn);
    }
  }

  const sampleButtons = document.querySelectorAll('#devtest-button-samples button.ui.button');
  if (sampleButtons.length) {
    const buttonStyles = document.querySelectorAll<HTMLInputElement>('input[name*="button-style"]');
    for (const elStyle of buttonStyles) {
      elStyle.addEventListener('click', () => {
        for (const btn of sampleButtons) {
          for (const el of buttonStyles) {
            if (el.value) btn.classList.toggle(el.value, el.checked);
          }
        }
      });
    }
    const buttonStates = document.querySelectorAll<HTMLInputElement>('input[name*="button-state"]');
    for (const elState of buttonStates) {
      elState.addEventListener('click', () => {
        for (const btn of sampleButtons) {
          (btn as any)[elState.value] = elState.checked;
        }
      });
    }
  }
}

export function initDevtest() {
  registerGlobalInitFunc('initDevtestPage', initDevtestPage);
}
