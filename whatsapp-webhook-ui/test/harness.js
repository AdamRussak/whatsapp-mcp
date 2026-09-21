// Test harness: a fresh page built from the REAL index.html and script.js, per test.
//
// Nothing in script.js is changed or stubbed for testing. The page is parsed exactly as a
// browser would parse it, so a field missing from the HTML fails the test instead of
// silently rendering nothing — which is the failure a stale UI container produced twice.
// Only the network is replaced: fetch is answered from a route table and every call is
// recorded, so a test can assert on exactly what the page asked the bridge for.

'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('jsdom');

const UI_DIR = path.join(__dirname, '..');
const HTML = fs.readFileSync(path.join(UI_DIR, 'index.html'), 'utf8');
const SCRIPT = fs.readFileSync(path.join(UI_DIR, 'script.js'), 'utf8');

const DEFAULT_ROUTES = {
    '/api/webhooks': { body: { success: true, data: [] } },
    '/api/chats': { body: { success: true, data: [] } },
    '/api/downloads': { body: { success: true, data: [], total: 0 } },
};

// settle lets the page's pending promises and zero-delay timers run.
async function settle(window) {
    for (let i = 0; i < 10; i++) {
        await new Promise((resolve) => window.setTimeout(resolve, 0));
    }
}

// createPage returns { window, document, manager, calls, settle, close }.
//
// routes maps "METHOD /path" or "/path" to either { status, body } or a function
// (url, options) => { status, body }.
async function createPage(routes = {}) {
    const dom = new JSDOM(HTML, {
        url: 'http://localhost:8089/',
        // Scripts run, but with no `resources` option the page's own <script src> is never
        // fetched: the only script that runs is the one injected below.
        runScripts: 'dangerously',
        pretendToBeVisual: true,
    });
    const { window } = dom;

    // Wait for the document's own load to finish, so script.js's DOMContentLoaded listener
    // is registered after that event has fired and never builds a second manager.
    if (window.document.readyState !== 'complete') {
        await new Promise((resolve) => window.addEventListener('load', resolve));
    }

    const calls = [];
    window.fetch = async (input, options = {}) => {
        const url = new URL(input);
        const method = (options.method || 'GET').toUpperCase();
        calls.push({ url, method, body: options.body ? JSON.parse(options.body) : undefined });

        const handler = routes[`${method} ${url.pathname}`] ?? routes[url.pathname] ?? DEFAULT_ROUTES[url.pathname];
        const result = typeof handler === 'function' ? handler(url, options) : handler;
        const { status = 200, body = { success: true, data: [] } } = result || {};
        return { ok: status >= 200 && status < 300, status, json: async () => body };
    };

    // script.js logs heavily; keep test output readable.
    window.console.log = () => {};
    window.console.error = () => {};

    // Run script.js as a classic <script> element, as the browser does. That matters: a
    // top-level class in a classic script joins the realm's shared global scope, where a
    // later lookup can see it. window.eval would give the class a scope of its own and
    // hide it — which is the first thing this harness got wrong.
    const element = window.document.createElement('script');
    element.textContent = SCRIPT;
    window.document.body.appendChild(element);
    const WebhookManager = window.eval('WebhookManager');
    const manager = new WebhookManager();
    await settle(window);

    return {
        window,
        document: window.document,
        manager,
        calls,
        settle: () => settle(window),
        close: () => window.close(),
    };
}

// callsTo returns the recorded calls to a path, in order.
function callsTo(calls, pathname, method = 'GET') {
    return calls.filter((c) => c.url.pathname === pathname && c.method === method);
}

module.exports = { createPage, callsTo };
