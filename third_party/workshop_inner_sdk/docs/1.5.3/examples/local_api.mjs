// Node.js 18.17+ standard-library example: pair, chat, or speak supplied text directly.
// Usage: node local_api.mjs http://DEVICE_IP:8081 "Hello" --credentials credentials.json
// Do not include /v1/api in the device URL. Enter the pairing code shown on the device on the first run; subsequent runs reuse the saved credentials.
// Query this turn by turn_id; a new turn replaces the active conversation.
// Use --speech device for a spoken chat reply, or --tts to read the supplied text directly.
import { createHash, createHmac, randomUUID } from 'node:crypto'
import { open, readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { createInterface } from 'node:readline/promises'
import { setTimeout as sleep } from 'node:timers/promises'
import { pathToFileURL } from 'node:url'
import { parseArgs } from 'node:util'

export class LocalAPI {
  constructor(baseURL, credentials = {}) {
    this.baseURL = baseURL.replace(/\/$/, '')
    const url = new URL(this.baseURL)
    if (!['http:', 'https:'].includes(url.protocol) || url.pathname !== '/' || url.search || url.hash) {
      throw new Error("Use a device URL such as http://DEVICE_IP:8081, without an API path or query parameters.")
    }
    this.credentials = credentials
  }

  headers(method, path, body = '', mode = 'empty') {
    const timestamp = String(Math.floor(Date.now() / 1000))
    const nonce = randomUUID()
    const userID = this.credentials.user_id || ''
    // Use the UTF-8 bytes of the original local_secret string; do not decode it as hexadecimal.
    const bodyHash = createHash('sha256').update(body).digest('hex')
    const canonical = [method.toUpperCase(), decodeURIComponent(new URL(path, this.baseURL).pathname), timestamp, nonce, mode, bodyHash, userID].join('|')
    const headers = {
      'X-Local-Sig': createHmac('sha256', this.credentials.local_secret).update(canonical).digest('hex'),
      'X-Local-Timestamp': timestamp, 'X-Local-Nonce': nonce, 'X-Local-Payload-Mode': mode,
      'X-Local-Body-Hash': bodyHash, 'X-Local-User-Id': userID
    }
    if (this.credentials.local_session_id) headers['X-Local-Session-Id'] = this.credentials.local_session_id
    return headers
  }

  async request(method, path, payload, { publicRequest = false, timeout = 30000 } = {}) {
    method = method.toUpperCase()
    const body = payload === undefined ? '' : JSON.stringify(payload)
    const mode = payload === undefined ? 'empty' : 'json'
    const headers = publicRequest ? {} : this.headers(method, path, body, mode)
    if (mode === 'json') headers['Content-Type'] = 'application/json'
    const response = await fetch(this.baseURL + path, {
      method, headers, body: method === 'GET' ? undefined : body, signal: AbortSignal.timeout(Math.ceil(timeout))
    })
    const result = await response.json()
    if (!response.ok) {
      const error = new Error(`HTTP ${response.status}: ${JSON.stringify(result)}`)
      error.status = response.status
      error.payload = result // Preserve structured error details; do not automatically retry writes.
      throw error
    }
    return result
  }

  async pair(readCode, authScope = 'session') {
    const { data: bootstrap } = await this.request('GET', '/v1/api/auth/bootstrap', undefined, { publicRequest: true })
    const { data: challenge } = await this.request('POST', '/v1/api/auth/pair/request', undefined, { publicRequest: true })
    const { data } = await this.request('POST', '/v1/api/auth/pair/confirm', {
      challenge_id: challenge.challenge_id, pairing_code: await readCode(), auth_scope: authScope
    }, { publicRequest: true })
    this.credentials = {
      local_secret: data.local_secret, user_id: bootstrap.user_id || '', auth_scope: data.auth_scope,
      local_session_id: data.local_session_id, local_session_expires_at_ms: data.local_session_expires_at_ms
    }
    return this.credentials
  }

  async speak(text, timeout = 180) {
    const { data: { slot_number: slot } } = await this.request('GET', '/v1/api/data/user/slot')
    const path = '/v1/api/data/user/tts/utterances'
    const { data: { utterance_id: utteranceID } } = await this.request('POST', path, {
      request_id: randomUUID(), slot, text
    })
    console.log("Speech submitted. Utterance ID: " + utteranceID)
    return this.waitForCompletion(`${path}/${utteranceID}`, timeout)
  }

  async chat(text, timeout = 120, speech = 'none') {
    if (!['none', 'device'].includes(speech)) throw new Error("Speech output must be none or device.")
    const { data: { slot_number: slot } } = await this.request('GET', '/v1/api/data/user/slot')
    const { data: { turn_id: turnID } } = await this.request('POST', '/v1/api/data/user/chat/turns', {
      request_id: randomUUID(), slot,
      input: { type: 'text', text }, output: { speech }
    })
    console.log("Chat submitted. Turn ID: " + turnID)
    const task = await this.waitForCompletion(`/v1/api/data/user/chat/turns/${turnID}`, timeout)
    return [task.assistant_text]
  }

  async waitForCompletion(path, timeout) {
    const deadline = performance.now() + timeout * 1000
    while (true) {
      const remaining = deadline - performance.now()
      if (remaining <= 0) throw new Error("Task timed out: " + path)
      const { data: task } = await this.request('GET', path, undefined,
        { timeout: Math.min(30000, remaining) })
      if (task.state === 'completed') return task
      if (['failed', 'cancelled'].includes(task.state)) {
        const code = task.error?.code || task.state
        const reason = task.error?.reason ? " (" + task.error.reason + ")" : ""
        throw new Error("Task stopped [" + code + "]" + reason + ": " + (task.error?.message || task.state))
      }
      await sleep(Math.min(1000, Math.max(0, deadline - performance.now())))
    }
  }
}

async function main() {
  const { positionals, values } = parseArgs({ allowPositionals: true, options: {
    credentials: { type: 'string', default: 'credentials.json' },
    'auth-scope': { type: 'string', default: 'session' },
    timeout: { type: 'string', default: '120' },
    speech: { type: 'string', default: 'none' },
    tts: { type: 'boolean', default: false },
    help: { type: 'boolean', short: 'h' }
  } })
  if (values.help) {
    console.log("Usage: node local_api.mjs http://DEVICE_IP:8081 \"Hello\" --credentials credentials.json")
    console.log("Do not include /v1/api in the device URL. Enter the pairing code shown on the device on the first run; subsequent runs reuse the saved credentials.")
    console.log("Query this turn by turn_id; a new turn replaces the active conversation.")
    console.log("Options: --credentials credentials file; --auth-scope session or permanent; --timeout wait in seconds (default: 120); --speech none or device (default: none); --tts read the supplied text directly on the device without a chat reply.")
    return
  }
  if (positionals.length !== 2 || !positionals[1].trim()) {
    throw new Error("Provide a device URL and nonempty text. Use --help for usage.")
  }
  const timeout = Number(values.timeout)
  if (!['session', 'permanent'].includes(values['auth-scope']) || !Number.isFinite(timeout) || timeout <= 0) {
    throw new Error("Auth scope must be session or permanent; timeout must be a finite positive number.")
  }
  if (!['none', 'device'].includes(values.speech)) throw new Error("Speech output must be none or device.")
  if (values.tts && values.speech !== 'none') throw new Error("Use --tts for direct speech or --speech device for a chat reply.")
  const api = new LocalAPI(positionals[0])
  try {
    api.credentials = JSON.parse(await readFile(values.credentials, 'utf8'))
  } catch (error) {
    if (error.code !== 'ENOENT') throw error
    await api.pair(async () => {
      const terminal = createInterface({ input: process.stdin, output: process.stdout })
      try {
        return (await terminal.question("Enter the pairing code shown on the device: ")).trim()
      } finally {
        terminal.close()
      }
    }, values['auth-scope'])
    // Create the credentials file exclusively with owner-only read/write permissions; never overwrite an existing file.
    const file = await open(values.credentials, 'wx', 0o600)
    try {
      await file.writeFile(JSON.stringify(api.credentials, null, 2) + '\n', 'utf8')
    } finally {
      await file.close()
    }
    console.log("Pairing succeeded. Credentials saved.")
  }
  if (values.tts) {
    await api.speak(positionals[1].trim(), timeout)
    console.log("Speech playback completed.")
  } else {
    for (const reply of await api.chat(positionals[1].trim(), timeout, values.speech)) console.log("Assistant reply: " + reply)
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch(error => {
    console.error(error.message)
    process.exitCode = 1
  })
}
