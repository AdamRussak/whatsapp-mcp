'use strict';

const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createPage, callsTo } = require('./harness');

const $ = (page, id) => page.document.getElementById(id);

function tickAutoDownload(page, on = true) {
    $(page, 'autoDownload').checked = on;
    $(page, 'autoDownload').dispatchEvent(new page.window.Event('change'));
}

async function submit(page, { retention = '' } = {}) {
    $(page, 'webhookName').value = 'Capture voice notes';
    $(page, 'webhookURL').value = '';
    tickAutoDownload(page);
    $(page, 'retentionDays').value = retention;
    $(page, 'webhookForm').dispatchEvent(new page.window.Event('submit', { cancelable: true }));
    await page.settle();
    return callsTo(page.calls, '/api/webhooks', 'POST').at(-1).body;
}

test('the retention field appears only with auto-download', async (t) => {
    const page = await createPage();
    t.after(page.close);

    assert.equal($(page, 'retentionGroup').style.display, 'none');
    tickAutoDownload(page);
    assert.equal($(page, 'retentionGroup').style.display, '');
    tickAutoDownload(page, false);
    assert.equal($(page, 'retentionGroup').style.display, 'none');
});

test('a retention period is sent as a number of days', async (t) => {
    const page = await createPage({ 'POST /api/webhooks': { body: { success: true, data: { id: 1 } } } });
    t.after(page.close);
    assert.equal((await submit(page, { retention: '30' })).retention_days, 30);
});

test('blank means keep forever, sent as 0', async (t) => {
    // 0 is what the bridge reads as "never remove". Anything else from a blank field —
    // NaN, null, an empty string — would be a bug in the one field that deletes files.
    const page = await createPage({ 'POST /api/webhooks': { body: { success: true, data: { id: 1 } } } });
    t.after(page.close);
    assert.equal((await submit(page, { retention: '' })).retention_days, 0);
});

test('editing a listener shows its retention, and blank for forever', async (t) => {
    const page = await createPage();
    t.after(page.close);
    const listener = {
        id: 10, name: 'Capture', webhook_url: '', enabled: true, auto_download: true,
        auto_download_voice_only: true, triggers: [{ trigger_type: 'all', trigger_value: '', match_type: 'exact', enabled: true }],
    };

    page.manager.populateForm({ ...listener, retention_days: 30 });
    assert.equal($(page, 'retentionDays').value, '30');
    assert.equal($(page, 'retentionGroup').style.display, '');

    page.manager.populateForm({ ...listener, retention_days: 0 });
    assert.equal($(page, 'retentionDays').value, '');
});

test('the form says when retention is only a dry run', async (t) => {
    const page = await createPage({
        '/api/health': { body: { success: true, retention: { enabled: true, dry_run: true } } },
    });
    t.after(page.close);
    tickAutoDownload(page);
    assert.match($(page, 'retentionHint').textContent, /Dry run is on/);
});

test('the form says when retention is switched off', async (t) => {
    const page = await createPage({
        '/api/health': { body: { success: true, retention: { enabled: false } } },
    });
    t.after(page.close);
    tickAutoDownload(page);
    assert.match($(page, 'retentionHint').textContent, /switched off/);
});

test('a removal renders as REMOVED with the file and the period', async (t) => {
    const page = await createPage();
    t.after(page.close);

    const entry = page.manager.createLogEntry({
        id: 1, webhook_config_id: 10, message_id: 'OLD', chat_jid: 'c@lid',
        payload: JSON.stringify({ status: 'pruned', path: 'media/972547669192/OLD.ogg', retention_days: 30 }),
        response_status: 0, response_body: 'media/972547669192/OLD.ogg', attempt_count: 1,
        delivered_at: '2026-10-30T12:00:00Z', created_at: '2026-10-30T12:00:00Z', event_type: 'retention',
    });
    assert.equal(entry.querySelector('.status-badge').textContent, 'REMOVED');
    assert.equal(entry.querySelector('.log-response').textContent, 'media/972547669192/OLD.ogg removed after 30 days');
    assert.doesNotMatch(entry.querySelector('.log-response').textContent, /HTTP|Delivered/);
});

test('a removed file is listed as removed, with nothing to play', async (t) => {
    const page = await createPage({
        '/api/downloads': {
            body: {
                success: true, total: 1, data: [{
                    message_id: 'OLD', chat_jid: 'c@lid', chat_name: 'Dana', status: 'pruned',
                    timestamp: '2026-09-01T10:00:00Z', seconds: 4, error: 'removed after 30 days by "Capture"',
                }],
            },
        },
    });
    t.after(page.close);
    $(page, 'viewDownloadsBtn').click();
    await page.settle();

    const row = page.document.querySelector('#downloadsBody tr');
    assert.equal(row.querySelector('audio'), null);
    assert.equal(row.querySelector('.col-play').textContent, 'Removed by retention');
    assert.match(row.querySelector('.col-play').title, /removed after 30 days/);
});
