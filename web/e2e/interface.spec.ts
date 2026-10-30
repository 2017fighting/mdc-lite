import { expect, test } from '@playwright/test'

// The interface smoke the acceptance environment runs: three views render over the
// run's own data, and the needs-number flow really fills a number in and re-runs the
// task.
//
// What it asserts is deliberately shallow — "the view renders, the data is the run's,
// the flow goes through" — because the deeper claims (the REST contract, the query
// building, the form's rules) are already asserted where they live: the Go contract
// tests and the frontend's jsdom tests. This is the one check that the binary serves
// an interface that boots, and it is not repeated in the fast gate.

/** required fails loudly when the acceptance run did not pass a setting through. */
function required(name: string): string {
  const value = process.env[name]
  if (!value) {
    throw new Error(`${name} is not set: this spec is driven by acceptance/acceptance_test.go`)
  }
  return value
}

const BASE = required('MDC_E2E_BASE_URL')
const NUMBER = required('MDC_E2E_NUMBER')
const TASK_ID = required('MDC_E2E_TASK_ID')
const CORPUS_DIR = required('MDC_E2E_CORPUS_DIR')
const WAITING_FILE = required('MDC_E2E_WAITING_FILE')
const WAITING_SECOND = required('MDC_E2E_WAITING_SECOND_FILE')
const WAITING_PATH = required('MDC_E2E_WAITING_PATH')
const WAITING_SECOND_PATH = required('MDC_E2E_WAITING_SECOND_PATH')
const WAITING_NUMBER = required('MDC_E2E_WAITING_NUMBER')

// Serial: the needs-number test changes the state the list tests read, and the view
// tests share one run's data.
test.describe.configure({ mode: 'serial' })


test('the scan view renders the directory deck over the run\'s own filesystem', async ({ page }) => {
  await page.goto(`${BASE}/scan`)
  await expect(page.getByRole('heading', { name: '扫描预览' })).toBeVisible()

  // The deck opens on the first browse root and lists its children: `library-in` is
  // the corpus root of this run, so finding it means the browser is looking at the
  // same filesystem the pipeline just read.
  await expect(page.getByRole('table', { name: /子目录/ })).toBeVisible()
  // The row is the browser's view of one directory on disk; the corpus root's name is
  // how the arm says "this is the same filesystem the pipeline just read".
  await expect(page.getByRole('row').filter({ hasText: `${CORPUS_DIR}/` })).toBeVisible()

  // The four-state toggle and the two-column Scope Ledger are the deck's whole point,
  // so both have to be on the page rather than in a modal.
  await expect(page.getByRole('button', { name: '加入扫描' }).first()).toBeVisible()
  await expect(page.getByRole('heading', { name: '待扫描目录清单' })).toBeVisible()
  await expect(page.getByRole('heading', { name: '排除过滤清单（整树跳过）' })).toBeVisible()
})

test('the task list renders the run\'s rows with their status', async ({ page }) => {
  await page.goto(`${BASE}/tasks`)
  await expect(page.getByRole('table', { name: /任务列表/ })).toBeVisible()

  const row = page.getByRole('row').filter({ hasText: NUMBER }).first()
  await expect(row).toBeVisible()
  // The pill's wording is the frontend's own; what matters is that one of the seven
  // statuses is on the row rather than an empty cell.
  await expect(row).toContainText(/成功|部分|失败|运行中|排队|待补号|已取消/)
})

test('the task detail renders the four trace tabs over the same task', async ({ page }) => {
  await page.goto(`${BASE}/tasks/${TASK_ID}`)
  for (const label of ['字段与来源', '步骤留痕', '源留痕', '落盘台账']) {
    await expect(page.getByRole('tab', { name: label })).toBeVisible()
  }
  // The step trace is the tab that proves the run's own `step_runs` reached the
  // browser: `scrape` is a step type, not a translation the UI could have invented.
  await page.getByRole('tab', { name: '步骤留痕' }).click()
  await expect(page.getByRole('tabpanel')).toContainText('scrape')
})

test('the needs-number panel fills a number and the task leaves the queue', async ({ page }) => {
  await page.goto(`${BASE}/numbers`)
  // `exact` because the waiting task's own card is headed "… — 待补号 …": the page's
  // title is the h1, and the card is not it.
  await expect(page.getByRole('heading', { name: '待补号', exact: true })).toBeVisible()

  const panel = page.getByRole('region', { name: '待补号' })
  await expect(panel).toBeVisible()

  // Each row is keyed by its path in the DOM, which is the only stable handle: the
  // labels are the file names, and a duplicate stem's two files differ exactly there.
  const first = panel.locator(`div[data-path="${WAITING_PATH}"]`)
  const second = panel.locator(`div[data-path="${WAITING_SECOND_PATH}"]`)
  await expect(first).toContainText(WAITING_FILE)
  await expect(second).toContainText(WAITING_SECOND)

  await first.getByRole('textbox').fill(WAITING_NUMBER)
  await second.getByRole('textbox').fill(WAITING_NUMBER)
  // The second file is the same work's second part: that checkbox is what makes the
  // submission one release instead of a conflict.
  await second.getByRole('checkbox').check()

  const submit = panel.getByRole('button', { name: '提交并重跑' })
  await expect(submit).toBeEnabled()
  await submit.click()

  // The panel disappears when the server has taken the numbers and the task has left
  // `needs_number`; the empty state is the page's own answer to that.
  await expect(page.getByText('没有待补号的任务')).toBeVisible({ timeout: 30_000 })
})
