// A log the app wrote about a call, such as a response body its model could not
// read. It shares the call's request id and is not one of its phases, so every
// screen that shows the call lists it, and none of them lets it change the call.

const { test, before, after } = require('node:test')
const assert = require('node:assert')

const { BASE, launch, linger, signIn, createProject, seedLogs } = require('./harness')

let browser, page, projectID, secret

const SID = '30000000-0000-4000-8000-000000000001'
const CART = '30000000-0000-4000-8000-000000000002'
// The app logged against this id, and none of its phases were stored.
const ORDERS = '30000000-0000-4000-8000-000000000003'

const DECODE_ERROR = "type 'int' is not a subtype of type 'double' in type cast"

const rows = () => page.locator('[data-network-row]')
const pane = () => page.locator('#network-detail')

before(async () => {
  ;({ browser, page } = await launch())
  await signIn(page)
  // Its own project: the shared fixtures elsewhere assert exact counts.
  ;({ id: projectID, secret } = await createProject(page, 'Linked logs E2E'))

  const now = Date.now()
  const at = ms => new Date(now - ms)
  const phase = (callPhase, ms, metadata) => ({
    sessionID: SID,
    message: `Network ${callPhase}`,
    level: 'debug',
    network: true,
    requestID: CART,
    callPhase,
    tags: ['network'],
    at: at(ms),
    metadata,
  })

  await seedLogs(
    projectID,
    secret,
    [
      phase('request', 60_000, { method: 'GET', url: 'https://api.test.dev/v2/cart' }),
      phase('response', 59_882, {
        status_code: 200,
        body: { subtotal: 49 },
        request: { method: 'GET', url: 'https://api.test.dev/v2/cart' },
      }),
      {
        sessionID: SID, level: 'error', at: at(59_880), requestID: CART,
        message: 'Could not read GET /v2/cart', error: DECODE_ERROR,
      },
      {
        sessionID: SID, level: 'error', at: at(30_000), requestID: ORDERS,
        message: 'Could not read GET /v2/orders', error: DECODE_ERROR,
      },
    ],
    [{ id: SID, deviceModel: 'Pixel 7', osName: 'Android', osVersion: '14',
       startedAt: at(65_000), lastSeenAt: at(25_000) }],
  )
})

after(async () => {
  await linger(page)
  if (browser) await browser.close()
})

test('the inspector lists the app log above the tabs, and the call stays a complete 200', async () => {
  await page.goto(`${BASE}/project/${projectID}/network?rid=${CART}`)
  await page.waitForSelector('#network-detail [data-linked-logs]')

  assert.equal(await rows().count(), 1, 'an app log made a row of its own')
  const row = await rows().first().textContent()
  assert.match(row, /200/)
  assert.match(row, /118ms/)
  const summary = (await page.locator('h1', { hasText: 'Network' }).locator('..').textContent()).trim()
  assert.match(summary, /1 call$/, `want "1 call" with nothing failed, got: ${summary}`)
  for (const chip of [rows().first(), pane()]) {
    const cls = await chip.locator('span', { hasText: /^200$/ }).first().getAttribute('class')
    assert.ok(cls.includes('text-[#3FB950]'), `the 200 is not green: ${cls}`)
  }

  const section = pane().locator('[data-linked-logs]')
  assert.match(await section.textContent(), /Logged by the app/)
  assert.match(await section.textContent(), /Could not read GET \/v2\/cart/)
  assert.ok((await section.textContent()).includes(DECODE_ERROR), 'the error text is missing')

  const beforeTabs = await page.evaluate(() => {
    const s = document.querySelector('#network-detail [data-linked-logs]')
    const t = document.querySelector('#network-detail [data-phase-tab]')
    return !!(s.compareDocumentPosition(t) & Node.DOCUMENT_POSITION_FOLLOWING)
  })
  assert.ok(beforeTabs, 'the section is not between the header and the tabs')

  // The call's tabs are its phases' and nothing else: no Error tab for an
  // app error, and the response body is the one the phase carried.
  const tabs = await pane().locator('[data-phase-tab]').evaluateAll(els => els.map(e => e.dataset.phaseTab))
  assert.deepEqual(tabs, ['headers', 'response'])
  assert.match(await pane().textContent(), /"subtotal": 49/)

  // Still there on another tab, since it sits above them.
  await pane().locator('[data-phase-tab="headers"]').click()
  await page.waitForSelector('#network-detail [data-phase-tab="headers"][aria-current="page"]')
  assert.ok(await pane().locator('[data-linked-logs]').isVisible(), 'the section left with the tab')

  // No count on the link: the log viewer it opens lists the phases as well.
  const all = pane().locator('[data-linked-logs-all]')
  assert.equal((await all.textContent()).trim(), 'Every log for this call')
  assert.equal(await all.getAttribute('href'), `/project/${projectID}/logs?q=request%3A${CART}`)
})

test('request:<id> in the log viewer lists the phases and the app log together', async () => {
  await page.goto(`${BASE}/project/${projectID}/network?rid=${CART}`)
  await Promise.all([
    page.waitForURL(/\/logs\?q=request/),
    pane().locator('[data-linked-logs-all]').click(),
  ])
  await page.waitForSelector('[data-log-row]')

  const logs = page.locator('[data-log-row]')
  assert.equal(await logs.count(), 3)
  const text = await page.locator('#log-rows').textContent()
  assert.match(text, /Could not read GET \/v2\/cart/)
  assert.doesNotMatch(text, /\/v2\/orders/, 'another call\'s log matched this request id')
})

test('the call page gives the app log a section, not a panel with no tab', async () => {
  await page.goto(`${BASE}/project/${projectID}/network/${CART}`)
  await page.waitForSelector('#phase-tabs')

  assert.equal(await page.locator('#phase-tabs button').count(), 2, 'want Request and Response only')
  assert.equal(await page.locator('#phase-tabs [id^="tab-"]').count(), 2, 'a panel with no tab to open it')
  assert.doesNotMatch(await page.locator('#phase-tabs').textContent(), /Could not read/)

  const section = page.locator('[data-linked-logs]')
  assert.ok(await section.isVisible())
  assert.match(await section.textContent(), /Could not read GET \/v2\/cart/)
  assert.ok((await section.textContent()).includes(DECODE_ERROR))
  assert.equal(await page.locator('[data-call-not-captured]').count(), 0)
})

test('a call whose phases were never stored says so instead of drawing an empty call', async () => {
  await page.goto(`${BASE}/project/${projectID}/network/${ORDERS}`)
  await page.waitForSelector('[data-call-not-captured]')

  assert.equal((await page.locator('h1').first().textContent()).trim(), 'Call not captured')
  assert.ok((await page.textContent('body')).includes(`request ${ORDERS}`), 'the page does not name the request id')
  assert.match(await page.locator('[data-call-not-captured]').textContent(), /The SDK writes network logs at debug/)
  assert.match(await page.locator('[data-linked-logs]').textContent(), /Could not read GET \/v2\/orders/)
  assert.equal(await page.locator('#phase-tabs').count(), 0)

  // The inspector has no row for it, and says the same in its pane.
  await page.goto(`${BASE}/project/${projectID}/network?rid=${ORDERS}`)
  await page.waitForSelector('#network-detail [data-call-not-captured]')
  assert.equal(await rows().count(), 1, 'an uncaptured call became a row')
  assert.match(await pane().textContent(), /call not captured/)
  assert.match(await pane().locator('[data-linked-logs]').textContent(), /Could not read GET \/v2\/orders/)
  assert.equal(await pane().locator('[data-phase-tab]').count(), 0)
})

test('a session\'s app log links to its call', async () => {
  await page.goto(`${BASE}/project/${projectID}/session/${SID}`)
  await page.waitForSelector('text=Could not read GET /v2/cart')

  const row = page.locator('tr', { hasText: 'Could not read GET /v2/cart' }).first()
  const link = row.locator('a', { hasText: 'Inspect this call' })
  assert.equal(await link.getAttribute('href'), `/project/${projectID}/network/${CART}`)

  await Promise.all([page.waitForURL(new RegExp(`/network/${CART}$`)), link.click()])
  await page.waitForSelector('[data-linked-logs]')
  assert.match(await page.locator('[data-linked-logs]').textContent(), /Could not read GET \/v2\/cart/)

  // The one whose phases were never stored lands on a page that says so.
  await page.goto(`${BASE}/project/${projectID}/session/${SID}`)
  const orphan = page.locator('tr', { hasText: 'Could not read GET /v2/orders' }).first()
  assert.equal(await orphan.locator('a', { hasText: 'Inspect this call' }).getAttribute('href'),
    `/project/${projectID}/network/${ORDERS}`)
})

test('an app log in the log viewer links to its call', async () => {
  await page.goto(`${BASE}/project/${projectID}/logs?q=${encodeURIComponent('level:error')}`)
  await page.waitForSelector('[data-log-row]')

  const requestLink = text => page.locator('[data-log-row]', { hasText: text })
    .locator('xpath=following-sibling::div[1]').locator('a', { hasText: 'Request:' })

  const orphanLink = requestLink('Could not read GET /v2/orders')
  assert.equal(await orphanLink.count(), 1, 'an app log whose call was not captured has no link to it')
  assert.equal(await orphanLink.getAttribute('href'), `/project/${projectID}/network/${ORDERS}`)

  const row = page.locator('[data-log-row]', { hasText: 'Could not read GET /v2/cart' })
  await row.click()
  const link = requestLink('Could not read GET /v2/cart')
  assert.equal(await link.count(), 1, 'the app log has no link to its call')
  assert.equal(await link.getAttribute('href'), `/project/${projectID}/network/${CART}`)
  await Promise.all([page.waitForURL(new RegExp(`/network/${CART}$`)), link.click()])
  await page.waitForSelector('[data-linked-logs]')
  assert.match(await page.locator('[data-linked-logs]').textContent(), /Could not read GET \/v2\/cart/)
})

// One app log for each list item its model rejected. The call shows three and
// says how many there are, on the inspector and on the call page alike. Six,
// because the two phases and three app logs that are loaded make five.
test('a call the app logged against many times lists three and counts the rest', async () => {
  const many = await createProject(page, 'Linked logs, many')
  const sid = '30000000-0000-4000-8000-000000000004'
  const feed = '30000000-0000-4000-8000-000000000005'
  const now = Date.now()
  const at = ms => new Date(now - ms)
  const request = { method: 'GET', url: 'https://api.test.dev/v2/feed' }
  const logs = [
    { sessionID: sid, message: 'Network request', level: 'debug', network: true,
      requestID: feed, callPhase: 'request', at: at(60_000), metadata: request },
    { sessionID: sid, message: 'Network response', level: 'debug', network: true,
      requestID: feed, callPhase: 'response', at: at(59_900), metadata: { status_code: 200, request } },
  ]
  for (let i = 1; i <= 6; i++) {
    logs.push({ sessionID: sid, level: 'error', at: at(59_900 - i * 10), requestID: feed,
      message: `Could not read feed item ${i}`, error: DECODE_ERROR })
  }
  await seedLogs(many.id, many.secret, logs,
    [{ id: sid, deviceModel: 'Pixel 7', startedAt: at(65_000), lastSeenAt: at(50_000) }])

  for (const url of [`/project/${many.id}/network?rid=${feed}`, `/project/${many.id}/network/${feed}`]) {
    await page.goto(BASE + url)
    await page.waitForSelector('[data-linked-logs]')
    const section = page.locator('[data-linked-logs]')
    assert.equal(await section.locator('[data-linked-log]').count(), 3, `${url}: want three of the app's logs listed`)
    assert.equal((await section.locator('[data-linked-logs-count]').textContent()).trim(), '3 of 6', url)
    const text = await section.textContent()
    for (const i of [1, 2, 3]) assert.ok(text.includes(`Could not read feed item ${i}`), `${url}: item ${i} is missing`)
    for (const i of [4, 5, 6]) assert.ok(!text.includes(`Could not read feed item ${i}`), `${url}: item ${i} is past the three`)
  }
})

// An app at the default minimumLevel stores no network logs at all, so its
// Network screen has no rows, and asked about one of its calls it has to show
// the pane that says the call was not captured rather than the empty state.
test('a project that stored no calls still says a call was not captured', async () => {
  const bare = await createProject(page, 'Linked logs, no calls')
  const sid = '30000000-0000-4000-8000-000000000006'
  const other = '30000000-0000-4000-8000-000000000007'
  const lost = '30000000-0000-4000-8000-000000000008'
  const now = Date.now()
  const at = ms => new Date(now - ms)
  await seedLogs(bare.id, bare.secret, [
    { sessionID: sid, level: 'error', at: at(30_000), requestID: lost,
      message: 'Could not read GET /v2/orders', error: DECODE_ERROR },
    { sessionID: other, level: 'info', at: at(20_000), message: 'a later launch' },
  ], [
    { id: sid, deviceModel: 'Pixel 7', startedAt: at(40_000), lastSeenAt: at(25_000) },
    { id: other, deviceModel: 'Pixel 7', startedAt: at(22_000), lastSeenAt: at(15_000) },
  ])

  await page.goto(`${BASE}/project/${bare.id}/network?rid=${lost}`)
  await page.waitForSelector('#network-detail [data-call-not-captured]')
  assert.equal(await rows().count(), 0)
  assert.match(await pane().locator('[data-linked-logs]').textContent(), /Could not read GET \/v2\/orders/)
  assert.equal((await page.locator('[data-network-none]').textContent()).trim(), 'No network calls were stored.')
  assert.doesNotMatch(await page.textContent('body'), /No network calls yet|Add the Dio interceptor/)

  // Closing it leaves the screen without the call, which is the empty state.
  await Promise.all([page.waitForURL(/\/network\?rid=$/), page.click('[data-dismiss-inspector]')])
  await page.waitForSelector('text=No network calls yet')
  assert.equal(await page.locator('#network-detail').count(), 0)

  // The launch the app logged it in shows it the same way, and another does not.
  await page.goto(`${BASE}/project/${bare.id}/session/${sid}?tab=network&rid=${lost}`)
  await page.waitForSelector('#network-detail [data-call-not-captured]')
  assert.equal((await page.locator('[data-network-none]').textContent()).trim(),
    'No network calls were stored for this launch.')
  assert.doesNotMatch(await page.textContent('body'), /made no network calls/)

  await page.goto(`${BASE}/project/${bare.id}/session/${other}?tab=network&rid=${lost}`)
  await page.waitForSelector('text=This launch made no network calls.')
  assert.equal(await page.locator('[data-call-not-captured]').count(), 0)
})
