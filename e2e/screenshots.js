// Regenerates the screenshots in the README.
//
// Not hand-captured: a screenshot taken once drifts the moment the UI moves,
// and nobody notices because nobody re-opens the README. `make screenshots`
// seeds a throwaway instance through the real ingest endpoint and captures the
// live product, so refreshing them is one command rather than an afternoon.
//
// Run it with `make screenshots`, which supplies the server and database.

const { mkdirSync } = require('node:fs')
const { join } = require('node:path')
const { randomUUID } = require('node:crypto')

const { BASE, launch, signIn, createProject, seedLogs } = require('./harness')
const { DeviceSocket, serveDatabase } = require('./device')

// .github/assets, not docs/: docs/ is gitignored in this repo because it holds
// internal design notes, so anything the README points at has to live outside it.
//
// CS_SHOTS_OUT redirects the whole set somewhere else — the marketing site wants
// the same images, and a second capture script would drift from this one.
const OUT = process.env.CS_SHOTS_OUT || join(__dirname, '..', '.github', 'assets', 'screenshots')

// A believable slice of a real app rather than lorem: the value of the product
// is that these rows tell a story, and a screenshot of "test log 1..20" shows
// none of it.
const SESSION = randomUUID()
const INSTALL = randomUUID()
const PAY = randomUUID()
const CART = randomUUID()
const PROFILE = randomUUID()
const PING = randomUUID()

const ago = ms => new Date(Date.now() - ms)

function story() {
  const s = (message, level, at, extra = {}) =>
    ({ message, level, at, sessionID: SESSION, ...extra })

  return [
    s('App launched', 'info', ago(9 * 60_000), { tags: ['lifecycle'] }),
    s('Signing in', 'debug', ago(8.5 * 60_000), { tags: ['auth'] }),
    s('User signed in', 'info', ago(8.4 * 60_000), { tags: ['auth'], metadata: { method: 'oauth' } }),
    s('Cache lookup: hit', 'debug', ago(8 * 60_000), { tags: ['cache'] }),

    // A GET that answered.
    s('Network Request', 'debug', ago(7 * 60_000), {
      tags: ['network'], network: true, requestID: CART, callPhase: 'request',
      metadata: { method: 'GET', url: 'https://api.shop.dev/v2/cart', headers: { accept: 'application/json' } },
    }),
    s('Network Response', 'debug', ago(7 * 60_000 - 118), {
      tags: ['network'], network: true, requestID: CART, callPhase: 'response',
      metadata: { status_code: 200, body: { items: 3, subtotal_cents: 4999 } },
    }),

    s('Checkout started', 'info', ago(6 * 60_000), { tags: ['checkout'] }),

    // A 401, which is the interesting kind of success.
    s('Network Request', 'debug', ago(5 * 60_000), {
      tags: ['network'], network: true, requestID: PROFILE, callPhase: 'request',
      metadata: { method: 'GET', url: 'https://api.shop.dev/v2/user/profile' },
    }),
    s('Network Response', 'debug', ago(5 * 60_000 - 96), {
      tags: ['network'], network: true, requestID: PROFILE, callPhase: 'response',
      metadata: { status_code: 401, body: { error: 'token_expired' } },
    }),
    s('Token expired, refreshing', 'warning', ago(4.9 * 60_000), { tags: ['auth'] }),

    // And the one that failed, with a redacted header so the marker shows.
    s('Network Request', 'debug', ago(3 * 60_000), {
      tags: ['network', 'payments'], network: true, requestID: PAY, callPhase: 'request',
      metadata: {
        method: 'POST', url: 'https://api.shop.dev/v2/payments/confirm',
        headers: { 'content-type': 'application/json', authorization: '[redacted]' },
        body: { order_id: 'ord_8812f', amount_cents: 4999 },
      },
    }),
    // 401, not a timeout. The retry goes out with the token the refresh has
    // just failed to replace, so the payment endpoint refuses it the same way
    // /v2/user/profile did.
    //
    // The seed is a story on purpose: the screenshots in the README are read by
    // somebody deciding whether this is worth installing, and a session whose
    // rows do not explain each other shows the screens without showing what
    // they are for. A timeout here would be a different bug with no connection
    // to the 401 two rows above it.
    s('Network Response', 'debug', ago(2.5 * 60_000), {
      tags: ['network', 'payments'], network: true, requestID: PAY, callPhase: 'response',
      metadata: { status_code: 401, body: { error: 'token_expired' } },
    }),

    // A separate call that genuinely fails, so the Errors screen still has a
    // network group to show. Grouping network failures on method and path is
    // most of the point of that screen, and it cannot be demonstrated with
    // nothing failing.
    s('Network Request', 'debug', ago(2.45 * 60_000), {
      tags: ['network'], network: true, requestID: PING, callPhase: 'request',
      metadata: { method: 'POST', url: 'https://telemetry.shop.dev/v1/events' },
    }),
    s('Network Error', 'error', ago(2.42 * 60_000), {
      tags: ['network'], network: true, requestID: PING, callPhase: 'error',
      metadata: {
        method: 'POST', url: 'https://telemetry.shop.dev/v1/events',
        type: 'DioExceptionType.receiveTimeout',
        message: 'Receiving data timed out after 30000ms',
      },
    }),
    s('Payment declined', 'error', ago(2.4 * 60_000), {
      tags: ['checkout', 'payments'],
      error: 'PaymentException: card_declined',
      stackTrace: [
        { index: 0, method: 'PaymentService.confirm', path: 'lib/payments/service.dart', line: 88, column: 12 },
        { index: 1, method: 'CheckoutBloc._onConfirm', path: 'lib/checkout/bloc.dart', line: 141, column: 7 },
      ],
      metadata: { order: 'ord_8812f', total: 49.99, currency: 'GBP' },
    }),
    s('Checkout abandoned', 'warning', ago(2 * 60_000), { tags: ['checkout'] }),
    s('Unrecoverable state in cart', 'fatal', ago(90_000), { tags: ['checkout'] }),
  ]
}

// The other launches. Without these the Errors screen photographs every group
// at ×1, which is the exact opposite of what that screen is for: the claim
// beside it is that thousands of copies of one bug collapse into a single row
// with a count. One session also left Sessions and Devices as one-row screens.
//
// Deliberately not evenly spread. Real crash volume is lumpy, and a flat
// distribution reads as generated data even when nobody can say why.
const CROWD = [
  { model: 'Pixel 7', os: 'Android', osVersion: '14', app: '3.11.2', build: '418', user: 'ada@example.com', declines: 4, mins: 40 },
  { model: 'iPhone 15 Pro', os: 'iOS', osVersion: '17.5', app: '3.11.2', build: '418', user: 'joel@example.com', declines: 9, mins: 95 },
  { model: 'Pixel 6a', os: 'Android', osVersion: '13', app: '3.11.2', build: '418', user: null, declines: 2, mins: 160 },
  { model: 'Galaxy S23', os: 'Android', osVersion: '14', app: '3.10.4', build: '402', user: 'rin@example.com', declines: 6, mins: 220 },
  { model: 'iPhone 13', os: 'iOS', osVersion: '17.4', app: '3.10.4', build: '402', user: null, declines: 1, mins: 300 },
  { model: 'Pixel 7', os: 'Android', osVersion: '14', app: '3.11.2', build: '418', user: 'ada@example.com', declines: 3, mins: 480 },
]

// One installation id per entry, held outside the two functions so the sessions
// and the logs that belong to them agree. Devices rolls launches up by this, so
// generating it twice would double the device count.
const CROWD_IDS = CROWD.map(() => ({ install: randomUUID(), session: randomUUID() }))

const sessions = () => ([
  {
    id: SESSION,
    installationID: INSTALL,
    userID: 'ada@example.com',
    deviceModel: 'Pixel 7',
    osName: 'Android',
    osVersion: '14',
    appVersion: '3.11.2',
    buildNumber: '418',
    metadata: { plan: 'pro' },
    startedAt: ago(10 * 60_000),
    lastSeenAt: ago(60_000),
  },
  ...CROWD.map((d, i) => ({
    id: CROWD_IDS[i].session,
    installationID: CROWD_IDS[i].install,
    userID: d.user,
    deviceModel: d.model,
    osName: d.os,
    osVersion: d.osVersion,
    appVersion: d.app,
    buildNumber: d.build,
    startedAt: ago(d.mins * 60_000),
    lastSeenAt: ago((d.mins - 6) * 60_000),
  })),
])

// The same failure from the other launches. The message is identical every
// time and the order id moves, which is precisely the case fingerprinting
// exists for: these collapse to one row while the fatal and the network error
// stay separate.
function crowdLogs() {
  const out = []

  CROWD.forEach((d, i) => {
    const sid = CROWD_IDS[i].session
    const base = d.mins * 60_000

    out.push({
      message: 'App launched', level: 'info', at: ago(base), sessionID: sid, tags: ['lifecycle'],
    })

    for (let n = 0; n < d.declines; n++) {
      out.push({
        message: 'Payment declined',
        level: 'error',
        at: ago(base - (n + 1) * 20_000),
        sessionID: sid,
        tags: ['checkout', 'payments'],
        error: 'PaymentException: card_declined',
        stackTrace: [
          { index: 0, method: 'PaymentService.confirm', path: 'lib/payments/service.dart', line: 88, column: 12 },
          { index: 1, method: 'CheckoutBloc._onConfirm', path: 'lib/checkout/bloc.dart', line: 141, column: 7 },
        ],
        metadata: { order: `ord_${(0x8812f + i * 977 + n).toString(16)}`, total: 49.99, currency: 'GBP' },
      })
    }
  })

  return out
}

// What a paired phone pushes while somebody watches. Sent one frame at a time
// with a beat between, because the live stream is the one screen whose value is
// that rows arrive — a burst delivered in a single tick photographs identically
// to a static list.
const liveFrames = () => {
  const at = ms => new Date(Date.now() + ms).toISOString()
  const rid = randomUUID()
  const cart = randomUUID()
  return [
    { level: 'info', message: 'Reproducing: cart checkout', timestamp: at(0), tags: ['qa'] },
    { level: 'debug', message: 'Cart loaded from cache (3 items)', timestamp: at(40), tags: ['cache'] },
    {
      level: 'debug', message: 'Network Request', timestamp: at(70),
      is_network_call: true, request_id: cart, call_phase: 'request',
      method: 'GET', url: 'https://api.shop.dev/v2/cart',
    },
    {
      level: 'debug', message: 'Network Response', timestamp: at(148),
      is_network_call: true, request_id: cart, call_phase: 'response',
      method: 'GET', url: 'https://api.shop.dev/v2/cart', status_code: 200,
      metadata: { body: { items: 3, subtotal_cents: 4999 } },
    },
    { level: 'info', message: 'Checkout sheet opened', timestamp: at(190), tags: ['checkout'] },
    { level: 'verbose', message: 'Address validated locally', timestamp: at(220), tags: ['checkout'] },
    // Deliberately does not name a flag that also appears in the seeded
    // feature_flags table: the database capture waits on a cell by its text,
    // and a log row carrying the same string makes that wait ambiguous.
    { level: 'debug', message: 'Feature flags resolved for this build', timestamp: at(250), tags: ['flags'] },
    { level: 'info', message: 'Confirming payment', timestamp: at(280), tags: ['payments'] },
    {
      level: 'debug', message: 'Network Request', timestamp: at(310),
      is_network_call: true, request_id: rid, call_phase: 'request',
      method: 'POST', url: 'https://api.shop.dev/v2/payments/confirm',
      metadata: { headers: { authorization: '[redacted]' }, body: { amount_cents: 4999 } },
    },
    {
      level: 'debug', message: 'Network Response', timestamp: at(430),
      is_network_call: true, request_id: rid, call_phase: 'response',
      method: 'POST', url: 'https://api.shop.dev/v2/payments/confirm', status_code: 402,
      metadata: { body: { error: 'card_declined' } },
    },
    { level: 'warning', message: 'Retrying with saved card', timestamp: at(470), tags: ['checkout'] },
    { level: 'error', message: 'Payment declined', timestamp: at(510), tags: ['checkout', 'payments'] },
    { level: 'warning', message: 'Checkout abandoned by user', timestamp: at(560), tags: ['checkout'] },
    { level: 'debug', message: 'Cart restored from sync_queue', timestamp: at(600), tags: ['cache'] },
    { level: 'info', message: 'Returned to product list', timestamp: at(640), tags: ['lifecycle'] },
  ]
}

// The phone's own storage, as the device would describe it. Feature flags,
// because that is the honest reason to reach into a QA build's database: the
// bug only happens with checkout_v2 on, and nobody wants a new build to find out.
const phoneDatabase = {
  sources: {
    ok: true,
    sources: [
      { name: 'shop.db', kind: 'sql', writable: true },
      { name: 'shared_preferences', kind: 'kv', writable: true },
    ],
  },
  namespaces: {
    ok: true,
    namespaces: [
      { name: 'feature_flags', kind: 'table' },
      { name: 'cart_items', kind: 'table' },
      { name: 'sync_queue', kind: 'table' },
    ],
  },
  rows: {
    ok: true,
    page: {
      columns: [
        { name: 'key', type: 'TEXT', not_null: true, primary_key: true },
        { name: 'enabled', type: 'INTEGER' },
        { name: 'rollout', type: 'REAL' },
        { name: 'auth_token', type: 'TEXT', redacted: true },
      ],
      rows: [
        // 0, not 1. This is the row that explains why the failure only happens
        // on one install: it never turned the new checkout on, so it is still
        // running the path where any non-200 is reported as a declined card.
        // The database screenshot is the only one that can show that, which is
        // the whole reason it is in the README.
        [{ v: 'checkout_v2' }, { v: 0 }, { v: 0.25 }, { v: '[redacted]', ro: 'This column is redacted.' }],
        [{ v: 'live_tracking' }, { v: 0 }, { v: 0 }, { v: '[redacted]', ro: 'This column is redacted.' }],
        [{ v: 'apple_pay' }, { v: 1 }, { v: 1 }, { v: '[redacted]', ro: 'This column is redacted.' }],
        [{ v: 'promo_banner' }, { v: 0 }, { v: 0.5 }, { v: '[redacted]', ro: 'This column is redacted.' }],
      ],
      handles: [4, 5, 6, 7],
      has_more: false,
      stopped_for_size: false,
    },
    writable: true,
  },
}

// Fixed rather than random: the API changes guide quotes this id under its
// screenshot and in its MCP example.
const LINKED_CART = '5c67a6d7-7663-4274-9152-ce71784c3aa1'
const LINKED_SESSION = randomUUID()

function linkedLogStory() {
  const s = (message, level, at, extra = {}) =>
    ({ message, level, at, sessionID: LINKED_SESSION, ...extra })

  const call = (id, method, url, status, startMs, tookMs, body) => [
    s('Network Request', 'debug', ago(startMs), {
      tags: ['network'], network: true, requestID: id, callPhase: 'request',
      metadata: { method, url, headers: { accept: 'application/json' } },
    }),
    s('Network Response', 'debug', ago(startMs - tookMs), {
      tags: ['network'], network: true, requestID: id, callPhase: 'response',
      metadata: {
        status_code: status,
        headers: { 'content-type': 'application/json' },
        body,
        request: { method, url },
      },
    }),
  ]

  return [
    s('App launched', 'info', ago(300_000), { tags: ['lifecycle'] }),
    ...call(randomUUID(), 'GET', 'https://api.shop.dev/v2/user/profile', 200, 280_000, 84,
      { id: 'u_8812', name: 'Ada' }),
    ...call(randomUUID(), 'GET', 'https://api.shop.dev/v2/products?page=1', 200, 240_000, 142,
      { page: 1, products: 24 }),
    s('Cart opened', 'info', ago(190_000), { tags: ['cart'] }),
    ...call(LINKED_CART, 'GET', 'https://api.shop.dev/v2/cart', 200, 180_000, 118,
      { items: 3, subtotal: 49 }),
    s('Could not read GET /v2/cart', 'error', ago(180_000 - 121), {
      tags: ['cart'],
      requestID: LINKED_CART,
      error: "type 'int' is not a subtype of type 'double' in type cast",
      stackTrace: [
        { index: 0, method: 'Cart.fromJson', path: 'lib/cart/cart.dart', line: 21, column: 28 },
        { index: 1, method: 'CartRepository.load', path: 'lib/cart/cart_repository.dart', line: 34, column: 14 },
      ],
    }),
  ]
}

const linkedLogSessions = () => ([
  {
    id: LINKED_SESSION,
    installationID: randomUUID(),
    deviceModel: 'Pixel 7',
    osName: 'Android',
    osVersion: '14',
    appVersion: '3.11.2',
    buildNumber: '418',
    sdkVersion: '1.6.0',
    startedAt: ago(330_000),
    lastSeenAt: ago(50_000),
  },
])

async function shoot(page, name) {
  // A settle beat: the charts animate in, and catching one mid-transition looks
  // like a rendering bug rather than a product.
  await page.waitForTimeout(600)
  await page.screenshot({ path: join(OUT, `${name}.png`) })
  console.log(`  ${name}.png`)
}

async function main() {
  mkdirSync(OUT, { recursive: true })

  // 1440x900 at 2x: wide enough for the network split to show both columns, and
  // retina so the type is not mushy on a GitHub page or when the marketing site
  // renders it at half size.
  const { browser, page } = await launch({ width: 1440, height: 900 }, { deviceScaleFactor: 2 })

  await signIn(page)
  const project = await createProject(page, 'Shop')
  await seedLogs(project.id, project.secret, [...story(), ...crowdLogs()], sessions())

  console.log('Capturing:')

  await page.goto(`${BASE}/project/${project.id}/overview`)
  await page.waitForSelector('h1')
  await shoot(page, 'overview')

  await page.goto(`${BASE}/project/${project.id}/logs`)
  await page.waitForSelector('[data-log-row]')
  await shoot(page, 'logs')

  await page.goto(`${BASE}/project/${project.id}/network?rid=${PAY}`)
  await page.waitForSelector('[data-network-row]')
  await shoot(page, 'network')

  await page.goto(`${BASE}/project/${project.id}/errors`)
  await page.waitForSelector('h1')
  await shoot(page, 'errors')

  // The same screen with the top group opened. Collapsed, the errors list looks
  // like any grouped list; the stack trace underneath is the part that answers
  // "and where did it happen", so the docs want both states.
  // Named rather than .first(): only the errors the SDK caught carry a trace,
  // and the newest group is the fatal, which has none. Picking by position
  // opened a group with nothing under it and the capture timed out.
  const declined = page.locator('[data-error-group]', { hasText: 'Payment declined' }).first()
  await declined.locator('summary').click()
  await declined.locator('pre').waitFor({ state: 'visible' })
  await shoot(page, 'errors-expanded')

  await page.goto(`${BASE}/project/${project.id}/sessions`)
  await page.waitForSelector('h1')
  await shoot(page, 'sessions')

  // One launch end to end, which is the screen the "replay what they actually
  // did" claim rests on. Captured from the seeded session rather than by
  // clicking a row, so it cannot pick an empty one on a re-seed.
  await page.goto(`${BASE}/project/${project.id}/session/${SESSION}`)
  await page.waitForSelector('[data-session-tab="logs"]')
  await shoot(page, 'session-detail')

  await page.goto(`${BASE}/project/${project.id}/devices`)
  await page.waitForSelector('h1')
  await shoot(page, 'devices')

  await shootLive(page, project)
  await shootLinkedLog(page, project)

  await browser.close()
  console.log(`\nWritten to ${OUT}`)
}

// The two screens QA actually lives in, and the two that cannot be captured
// from seeded rows: both only exist while a device is on the other end of a
// socket. So this pairs one.
async function shootLive(page, project) {
  const dev = new DeviceSocket()
  await dev.connect({ 'X-Project-ID': project.id, 'X-Project-Secret': project.secret })

  // Minted through the real button, read off the real card — the same path a
  // dev takes when they read a code out to a tester.
  await page.goto(`${BASE}/project/${project.id}/live`)
  await page.waitForSelector('button:has-text("New session")')
  await page.click('button:has-text("New session")')
  await page.waitForSelector('[data-pairing-code]')
  const code = (await page.locator('[data-pairing-code]').first().innerText()).trim()

  dev.send({
    code,
    device: {
      session_id: randomUUID(),
      installation_id: randomUUID(),
      device_model: 'Pixel 7',
      os_name: 'Android',
      os_version: '14',
      app_version: '3.11.2',
    },
  })
  const reply = JSON.parse(await dev.next())
  if (!reply.ok) throw new Error(`pairing was refused: ${JSON.stringify(reply)}`)

  try {
    await page.goto(`${BASE}/project/${project.id}/live/${reply.session_id}`)
    // Publishing before the watcher has subscribed drops the frames into a
    // stream nobody is reading, and the shot comes back empty.
    await page.waitForSelector('#live-status:has-text("Connected")', { timeout: 10000 })

    for (const log of liveFrames()) {
      dev.send({ logs: [{ id: randomUUID(), ...log }] })
      await page.waitForTimeout(120)
    }
    await page.waitForSelector('#live-stream:has-text("Payment declined")', { timeout: 10000 })
    await shoot(page, 'live')

    const served = serveDatabase(dev, phoneDatabase)
    try {
      await page.click('[data-live-tab="db"]')
      await page.waitForSelector('text=feature_flags', { timeout: 8000 })
      await page.click('button:has-text("feature_flags")')
      await page.waitForSelector('text=checkout_v2', { timeout: 8000 })
      await shoot(page, 'database')
    } finally {
      served.stop()
    }
  } finally {
    dev.close()
  }
}

// A project of its own, so the list holds only this launch's three calls.
async function shootLinkedLog(page, readme) {
  // Project names are unique, and the API changes guide calls this one Shop.
  await page.goto(`${BASE}/project/${readme.id}/settings`)
  await page.fill('#general-form input[name="name"]', 'Shop README')
  await page.click('#general-form button[type="submit"]')
  await page.waitForSelector('#general-form:has-text("Saved")', { timeout: 5000 })

  const project = await createProject(page, 'Shop')
  await seedLogs(project.id, project.secret, linkedLogStory(), linkedLogSessions())

  await page.goto(`${BASE}/project/${project.id}/network?rid=${LINKED_CART}`)
  await page.waitForSelector('#network-detail [data-linked-logs]')
  await shoot(page, 'network-linked-log')
}

main().catch(err => {
  console.error(err)
  process.exit(1)
})
