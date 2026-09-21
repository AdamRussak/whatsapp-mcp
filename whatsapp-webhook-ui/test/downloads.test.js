'use strict';

const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createPage, callsTo } = require('./harness');

const item = (over = {}) => ({
    message_id: '3A1F647EBD8CE7F8018E',
    chat_jid: '62156783493249@lid',
    chat_name: 'אילן וולפסון',
    sender: '62156783493249',
    is_from_me: false,
    timestamp: '2026-09-21T11:38:50Z',
    seconds: 3,
    is_ptt: true,
    status: 'ok',
    size_bytes: 8840,
    media_path: '972547669192/2026-09-21_113850_them_3A1F647EBD8CE7F8018E.ogg',
    // The container DNS name: correct for the MCP server, unresolvable from a browser.
    download_url: 'http://whatsapp-mcp-whatsapp-bridge-1:8080/api/media/972547669192/2026-09-21_113850_them_3A1F647EBD8CE7F8018E.ogg',
    ...over,
});

async function openDownloads(page) {
    page.document.getElementById('viewDownloadsBtn').click();
    await page.settle();
}

const rows = (page) => [...page.document.querySelectorAll('#downloadsBody tr')];

test('the Downloads tab switches views and loads once', async (t) => {
    const page = await createPage({ '/api/downloads': { body: { success: true, data: [item()], total: 1 } } });
    t.after(page.close);

    assert.equal(page.document.getElementById('downloadsView').hidden, true);
    await openDownloads(page);

    assert.equal(page.document.getElementById('downloadsView').hidden, false);
    assert.equal(page.document.getElementById('webhooksView').hidden, true);
    assert.equal(page.document.getElementById('viewDownloadsBtn').getAttribute('aria-selected'), 'true');

    // Switching away and back does not refetch; Apply does.
    page.document.getElementById('viewWebhooksBtn').click();
    await openDownloads(page);
    assert.equal(callsTo(page.calls, '/api/downloads').length, 1);
});

test('a row plays and saves from the browser-reachable URL, not download_url', async (t) => {
    const page = await createPage({ '/api/downloads': { body: { success: true, data: [item()], total: 1 } } });
    t.after(page.close);
    await openDownloads(page);

    const [row] = rows(page);
    const expected = 'http://localhost:8080/api/media/972547669192/2026-09-21_113850_them_3A1F647EBD8CE7F8018E.ogg';
    assert.equal(row.querySelector('audio').src, expected);
    assert.equal(row.querySelector('audio').preload, 'none');
    assert.equal(row.querySelector('.col-save a').href, `${expected}?download=1`);
    assert.doesNotMatch(row.innerHTML, /whatsapp-mcp-whatsapp-bridge-1/);

    const cells = [...row.querySelectorAll('td')].map((td) => td.textContent);
    assert.equal(cells[1], 'אילן וולפסון');
    assert.equal(cells[3], '62156783493249');
    assert.equal(cells[4], '0:03');
    assert.equal(cells[5], '8.6 KB');
});

test('a chat name is rendered as text, never as markup', async (t) => {
    // Chat names and senders are set by other WhatsApp users.
    const hostile = '<img src=x onerror="window.__pwned = true">';
    const page = await createPage({
        '/api/downloads': { body: { success: true, data: [item({ chat_name: hostile, sender: hostile })], total: 1 } },
    });
    t.after(page.close);
    await openDownloads(page);

    const [row] = rows(page);
    assert.equal(row.querySelector('img'), null, 'the name must not become an element');
    assert.equal(row.querySelectorAll('td')[1].textContent, hostile);
    assert.equal(page.window.__pwned, undefined);
});

test('a missing file and a failed download are shown, not offered for playback', async (t) => {
    const page = await createPage({
        '/api/downloads': {
            body: {
                success: true, total: 2, data: [
                    item({ message_id: 'GONE', media_path: '', size_bytes: 0, file_missing: true }),
                    item({ message_id: 'EXPIRED', status: 'failed', media_path: '', size_bytes: 0, error: 'direct download expired' }),
                ],
            },
        },
    });
    t.after(page.close);
    await openDownloads(page);

    const [gone, expired] = rows(page);
    for (const row of [gone, expired]) {
        assert.equal(row.querySelector('audio'), null);
        assert.equal(row.querySelector('.col-save a'), null);
        assert.ok(row.classList.contains('download-unavailable'));
    }
    assert.equal(gone.querySelector('.col-play').textContent, 'File missing');
    assert.equal(expired.querySelector('.col-play').textContent, 'Failed');
    assert.equal(expired.querySelector('.col-play').title, 'direct download expired');
});

test('dates filter by local day, with To inclusive', async (t) => {
    // Runs under TZ=Asia/Jerusalem (UTC+3 in September). A bare date sent as-is would mean
    // UTC midnight and shift both edges of the day by three hours.
    const page = await createPage();
    t.after(page.close);
    await openDownloads(page);

    page.document.getElementById('downloadsFrom').value = '2026-09-20';
    page.document.getElementById('downloadsTo').value = '2026-09-20';
    page.document.getElementById('downloadsFilters').dispatchEvent(new page.window.Event('submit', { cancelable: true }));
    await page.settle();

    const last = callsTo(page.calls, '/api/downloads').at(-1).url.searchParams;
    assert.equal(last.get('after'), '2026-09-19T21:00:00.000Z', 'From = local midnight on the 20th');
    assert.equal(last.get('before'), '2026-09-20T21:00:00.000Z', 'To = local midnight on the 21st, so the 20th is included');
});

test('the pager hides for one page and walks older results', async (t) => {
    const many = Array.from({ length: 25 }, (_, i) => item({ message_id: `M${i}` }));
    const page = await createPage({
        '/api/downloads': (url) => ({
            body: { success: true, total: 60, data: url.searchParams.get('offset') === '50' ? many.slice(0, 10) : many },
        }),
    });
    t.after(page.close);
    await openDownloads(page);

    const pager = page.document.getElementById('downloadsPager');
    const range = page.document.getElementById('downloadsRange');
    assert.equal(pager.hidden, false);
    assert.equal(range.textContent, '1–25 of 60');
    assert.equal(page.document.getElementById('downloadsPrev').disabled, true);

    page.document.getElementById('downloadsNext').click();
    await page.settle();
    page.document.getElementById('downloadsNext').click();
    await page.settle();

    const offsets = callsTo(page.calls, '/api/downloads').map((c) => c.url.searchParams.get('offset'));
    assert.deepEqual(offsets, ['0', '25', '50']);
    assert.equal(range.textContent, '51–60 of 60');
    assert.equal(page.document.getElementById('downloadsNext').disabled, true);
});

test('the pager stays hidden when everything fits on one page', async (t) => {
    const page = await createPage({ '/api/downloads': { body: { success: true, data: [item()], total: 1 } } });
    t.after(page.close);
    await openDownloads(page);
    assert.equal(page.document.getElementById('downloadsPager').hidden, true);
});

test('an empty result says so instead of showing an empty table', async (t) => {
    const page = await createPage();
    t.after(page.close);
    await openDownloads(page);
    assert.equal(page.document.getElementById('downloadsTable').hidden, true);
    assert.equal(page.document.getElementById('noDownloads').hidden, false);
});
