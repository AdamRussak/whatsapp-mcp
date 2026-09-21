'use strict';

const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createPage, callsTo } = require('./harness');

const capture = {
    id: 11783,
    webhook_config_id: 10,
    message_id: '3EB0B2906ED284AE5BA937',
    chat_jid: '62156783493249@lid',
    trigger_type: 'media_type',
    trigger_value: 'audio',
    payload: JSON.stringify({ status: 'captured', detail: 'media/972547669192/x.ogg', duration_ms: 377, rule: 'Capture voice notes' }),
    response_status: 0,
    response_body: 'media/972547669192/x.ogg',
    attempt_count: 1,
    delivered_at: '2026-09-21T11:56:26Z',
    created_at: '2026-09-21T11:56:26Z',
    event_type: 'auto_download',
};

test('a capture log renders as CAPTURED with its path and duration', async (t) => {
    const page = await createPage();
    t.after(page.close);

    const entry = page.manager.createLogEntry(capture);
    assert.equal(entry.querySelector('.status-badge').textContent, 'CAPTURED');
    assert.equal(entry.querySelector('.log-response').textContent, 'media/972547669192/x.ogg (377ms)');
    // A capture has no HTTP status; it must not be dressed up as one.
    assert.doesNotMatch(entry.querySelector('.log-response').textContent, /HTTP/);
    // Its duration is the processing time, not 0ms.
    assert.equal(entry.querySelector('.log-processing-time').textContent, '377ms');
});

test('a failed capture renders as CAPTURE FAILED with its error', async (t) => {
    const page = await createPage();
    t.after(page.close);

    const failed = { ...capture, delivered_at: null, response_body: 'direct download expired' };
    const entry = page.manager.createLogEntry(failed);
    assert.equal(entry.querySelector('.status-badge').textContent, 'CAPTURE FAILED');
    assert.equal(entry.querySelector('.log-error-message').textContent, 'direct download expired');
});

test('a delivery log still renders as an HTTP delivery', async (t) => {
    const page = await createPage();
    t.after(page.close);

    const delivery = {
        ...capture,
        event_type: 'delivery',
        response_status: 200,
        response_body: '{"message":"ok"}',
        payload: JSON.stringify({ metadata: { processing_time_ms: 12 } }),
    };
    const entry = page.manager.createLogEntry(delivery);
    assert.equal(entry.querySelector('.status-badge').textContent, 'SUCCESS');
    assert.equal(entry.querySelector('.log-response').textContent, 'HTTP 200 - ok');
    assert.equal(entry.querySelector('.log-processing-time').textContent, '12ms');
});

test('ticking auto-download reveals voice-only and waives the URL requirement', async (t) => {
    const page = await createPage();
    t.after(page.close);
    const $ = (id) => page.document.getElementById(id);

    assert.equal($('voiceOnlyGroup').style.display, 'none');
    assert.equal($('webhookURLLabel').textContent, 'Webhook URL *');

    $('autoDownload').checked = true;
    $('autoDownload').dispatchEvent(new page.window.Event('change'));

    assert.equal($('voiceOnlyGroup').style.display, '');
    assert.equal($('webhookURLLabel').textContent, 'Webhook URL');
    assert.equal($('webhookURLHint').style.display, '');
});

// fillForm fills the add-webhook form and submits it.
async function submitForm(page, { name, url = '', autoDownload = false, voiceOnly = true }) {
    const $ = (id) => page.document.getElementById(id);
    $('webhookName').value = name;
    $('webhookURL').value = url;
    $('autoDownload').checked = autoDownload;
    $('autoDownloadVoiceOnly').checked = voiceOnly;
    $('webhookForm').dispatchEvent(new page.window.Event('submit', { cancelable: true }));
    await page.settle();
}

test('a download-only listener submits with no URL and the auto-download fields', async (t) => {
    const page = await createPage({ 'POST /api/webhooks': { body: { success: true, data: { id: 12 } } } });
    t.after(page.close);

    await submitForm(page, { name: 'Capture voice notes', autoDownload: true, voiceOnly: false });

    const posts = callsTo(page.calls, '/api/webhooks', 'POST');
    assert.equal(posts.length, 1, 'the form should have been submitted');
    assert.equal(posts[0].body.webhook_url, '');
    assert.equal(posts[0].body.auto_download, true);
    assert.equal(posts[0].body.auto_download_voice_only, false);
});

test('a listener with neither a URL nor auto-download is refused before any request', async (t) => {
    const page = await createPage();
    t.after(page.close);

    await submitForm(page, { name: 'Goes nowhere' });

    assert.equal(callsTo(page.calls, '/api/webhooks', 'POST').length, 0);
    assert.match(page.document.getElementById('toastMessage').textContent, /URL is required unless auto-download/);
});

test('a card shows the auto-download badge and a download-only URL placeholder', async (t) => {
    const page = await createPage();
    t.after(page.close);

    const card = page.manager.createWebhookCard({
        id: 10,
        name: 'Capture voice notes',
        webhook_url: '',
        enabled: true,
        auto_download: true,
        auto_download_voice_only: true,
        created_at: '2026-09-21T11:36:32Z',
        triggers: [{ trigger_type: 'media_type', trigger_value: 'audio', match_type: 'exact', enabled: true }],
    });

    const badge = card.querySelector('.webhook-autodownload');
    assert.notEqual(badge.style.display, 'none');
    assert.match(badge.title, /voice notes/);
    assert.equal(card.querySelector('.webhook-url').textContent, '— (download-only listener)');
});

test('an ordinary webhook card shows no badge', async (t) => {
    const page = await createPage();
    t.after(page.close);

    const card = page.manager.createWebhookCard({
        id: 8, name: 'poll-vote', webhook_url: 'https://n8n.example/webhook/poll-vote', enabled: true,
        auto_download: false, auto_download_voice_only: true, created_at: '2026-02-28T21:05:01Z',
        triggers: [{ trigger_type: 'media_type', trigger_value: 'poll_vote', match_type: 'exact', enabled: true }],
    });
    assert.equal(card.querySelector('.webhook-autodownload').style.display, 'none');
    assert.equal(card.querySelector('.webhook-url').textContent, 'https://n8n.example/webhook/poll-vote');
});
