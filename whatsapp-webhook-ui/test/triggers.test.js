'use strict';

// The listener form's Match selector, its plain-language sentence, and live validation
// against POST /api/webhooks/validate (ADR 0001, T6).

const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createPage, callsTo } = require('./harness');

const R3 = {
    rule: 'R3',
    trigger_index: 1,
    message: 'R3: A message belongs to one chat, so chat = A and chat = B can never both match.',
};

// A little over the form's 300ms debounce, then let the validate response land.
async function afterValidation(page) {
    await new Promise((resolve) => page.window.setTimeout(resolve, 350));
    await page.settle();
}

// setRow fills trigger row i and fires the events a user's edit would, which bubble to the form.
function setRow(page, i, { type, value = '', match = 'exact' }) {
    const row = page.document.querySelectorAll('#triggersContainer .trigger-item')[i];
    const typeSelect = row.querySelector('.trigger-type');
    typeSelect.value = type;
    typeSelect.dispatchEvent(new page.window.Event('change', { bubbles: true }));
    row.querySelector('.match-type').value = match;
    const input = row.querySelector('.trigger-value');
    input.value = value;
    input.dispatchEvent(new page.window.Event('input', { bubbles: true }));
}

function setMode(page, mode) {
    const select = page.document.getElementById('triggerMode');
    select.value = mode;
    select.dispatchEvent(new page.window.Event('change', { bubbles: true }));
}

function rows(page) {
    return page.document.querySelectorAll('#triggersContainer .trigger-item');
}

async function openNewListener(page, { name = 'Voice from A', url = 'https://n8n.example/hook' } = {}) {
    page.document.getElementById('addWebhookBtn').click();
    page.document.getElementById('webhookName').value = name;
    page.document.getElementById('webhookURL').value = url;
}

test('the form starts with one row that carries an error slot, and Match defaults to OR', async (t) => {
    const page = await createPage();
    t.after(page.close);

    await openNewListener(page);
    assert.equal(page.document.getElementById('triggerMode').value, 'or');
    assert.equal(rows(page).length, 1);
    assert.ok(rows(page)[0].querySelector('.trigger-error'), 'every row has a .trigger-error slot');

    page.document.getElementById('addTriggerBtn').click();
    assert.ok(rows(page)[1].querySelector('.trigger-error'), 'an added row has one too');
});

test('the payload carries trigger_mode', async (t) => {
    const page = await createPage({ 'POST /api/webhooks': { body: { success: true, data: { id: 1 } } } });
    t.after(page.close);

    await openNewListener(page);
    page.document.getElementById('webhookForm').dispatchEvent(new page.window.Event('submit', { cancelable: true }));
    await page.settle();
    assert.equal(callsTo(page.calls, '/api/webhooks', 'POST')[0].body.trigger_mode, 'or');

    await openNewListener(page);
    setMode(page, 'and');
    await afterValidation(page);
    page.document.getElementById('webhookForm').dispatchEvent(new page.window.Event('submit', { cancelable: true }));
    await page.settle();
    assert.equal(callsTo(page.calls, '/api/webhooks', 'POST')[1].body.trigger_mode, 'and');
});

test('the sentence reads the two templates', async (t) => {
    const page = await createPage();
    t.after(page.close);
    const summary = () => page.document.getElementById('triggerSummary').textContent;

    await openNewListener(page);
    setRow(page, 0, { type: 'media_type', value: 'audio' });
    page.document.getElementById('addTriggerBtn').click();
    setRow(page, 1, { type: 'chat_jid', value: 'A' });
    assert.equal(summary(), 'Fires for messages where media type is audio, or chat is A.');

    // AND, with the chat first
    setRow(page, 0, { type: 'chat_jid', value: 'A' });
    setRow(page, 1, { type: 'media_type', value: 'audio' });
    setMode(page, 'and');
    assert.equal(summary(), 'Fires for messages where chat is A, and media type is audio.');
});

test('"all" reads as every message under OR, and adds nothing under AND', async (t) => {
    const page = await createPage();
    t.after(page.close);
    const summary = () => page.document.getElementById('triggerSummary').textContent;

    await openNewListener(page);
    assert.equal(summary(), 'Fires for every message.');
    page.document.getElementById('addTriggerBtn').click();
    setRow(page, 1, { type: 'media_type', value: 'audio' });
    assert.equal(summary(), 'Fires for every message.');
    setMode(page, 'and');
    assert.equal(summary(), 'Fires for messages where media type is audio.');
});

test('a rejected body puts the message on the named row and disables Save', async (t) => {
    const page = await createPage({
        'POST /api/webhooks/validate': { status: 400, body: { success: false, error: R3.message, errors: [R3] } },
    });
    t.after(page.close);

    await openNewListener(page);
    setMode(page, 'and');
    setRow(page, 0, { type: 'chat_jid', value: 'A' });
    page.document.getElementById('addTriggerBtn').click();
    setRow(page, 1, { type: 'chat_jid', value: 'B' });
    await afterValidation(page);

    // The validate call sent what a save would, rows in order.
    const validates = callsTo(page.calls, '/api/webhooks/validate', 'POST');
    const last = validates[validates.length - 1].body;
    assert.equal(last.trigger_mode, 'and');
    assert.deepEqual(last.triggers.map((tr) => tr.trigger_value), ['A', 'B']);

    const [first, second] = rows(page);
    assert.equal(second.querySelector('.trigger-error').textContent, R3.message);
    assert.equal(second.querySelector('.trigger-error').hidden, false);
    assert.equal(first.querySelector('.trigger-error').textContent, '');
    assert.equal(page.document.getElementById('formErrors').hidden, true);
    assert.equal(page.document.getElementById('submitBtn').disabled, true);

    // Submitting anyway, which ignores a disabled button, still saves nothing.
    page.document.getElementById('webhookForm').dispatchEvent(new page.window.Event('submit', { cancelable: true }));
    await page.settle();
    assert.equal(callsTo(page.calls, '/api/webhooks', 'POST').length, 0);
});

test('a clean body enables Save', async (t) => {
    const page = await createPage({
        'POST /api/webhooks/validate': { body: { success: true, errors: [] } },
        'POST /api/webhooks': { body: { success: true, data: { id: 1 } } },
    });
    t.after(page.close);

    await openNewListener(page);
    setRow(page, 0, { type: 'media_type', value: 'audio' });
    // While the check is pending, Save waits for it.
    assert.equal(page.document.getElementById('submitBtn').disabled, true);

    await afterValidation(page);
    assert.equal(page.document.getElementById('submitBtn').disabled, false);
    assert.equal(rows(page)[0].querySelector('.trigger-error').hidden, true);

    page.document.getElementById('webhookForm').dispatchEvent(new page.window.Event('submit', { cancelable: true }));
    await page.settle();
    assert.equal(callsTo(page.calls, '/api/webhooks', 'POST').length, 1);
});

test('an error with trigger_index -1 is shown for the form, not on a trigger row', async (t) => {
    const R2 = { rule: 'R2', trigger_index: -1, message: 'R2: Auto-download only captures audio.' };
    const page = await createPage({
        'POST /api/webhooks/validate': { status: 400, body: { success: false, error: R2.message, errors: [R2] } },
    });
    t.after(page.close);

    await openNewListener(page);
    setRow(page, 0, { type: 'media_type', value: 'image' });
    await afterValidation(page);

    for (const row of rows(page)) {
        assert.equal(row.querySelector('.trigger-error').textContent, '');
        assert.equal(row.classList.contains('has-error'), false);
    }
    assert.equal(page.document.getElementById('formErrors').hidden, false);
    assert.equal(page.document.getElementById('formErrors').textContent, R2.message);
    assert.equal(page.document.getElementById('submitBtn').disabled, true);
});

test('empty rows are validated in place, so trigger_index still points at the row the user sees', async (t) => {
    const page = await createPage();
    t.after(page.close);

    await openNewListener(page);
    setRow(page, 0, { type: 'keyword', value: '' });
    page.document.getElementById('addTriggerBtn').click();
    setRow(page, 1, { type: 'chat_jid', value: 'A' });
    await afterValidation(page);

    const validates = callsTo(page.calls, '/api/webhooks/validate', 'POST');
    const triggers = validates[validates.length - 1].body.triggers;
    assert.equal(triggers.length, 2);
    assert.equal(triggers[0].trigger_value, '');
    assert.equal(triggers[1].trigger_value, 'A');
});

test('a 400 from a save draws its errors on the form', async (t) => {
    const page = await createPage({
        'POST /api/webhooks': { status: 400, body: { success: false, error: R3.message, errors: [R3] } },
    });
    t.after(page.close);

    await openNewListener(page);
    page.document.getElementById('addTriggerBtn').click();
    await afterValidation(page); // live validation found nothing; the save is what refuses
    page.document.getElementById('webhookForm').dispatchEvent(new page.window.Event('submit', { cancelable: true }));
    await page.settle();

    assert.equal(rows(page)[1].querySelector('.trigger-error').textContent, R3.message);
    assert.equal(page.document.getElementById('submitBtn').disabled, true);
    assert.match(page.document.getElementById('toastMessage').textContent, /can never both match/);
});

test('editing restores the mode and keeps a disabled trigger disabled', async (t) => {
    const page = await createPage({ 'PUT /api/webhooks/5': { body: { success: true, data: {} } } });
    t.after(page.close);

    page.manager.openModal({
        id: 5, name: 'Audio in A', webhook_url: 'https://n8n.example/hook', enabled: true, trigger_mode: 'and',
        triggers: [
            { trigger_type: 'chat_jid', trigger_value: 'A', match_type: 'exact', enabled: true },
            { trigger_type: 'keyword', trigger_value: 'x', match_type: 'contains', enabled: false },
            { trigger_type: 'media_type', trigger_value: 'audio', match_type: 'exact', enabled: true },
        ],
    });
    assert.equal(page.document.getElementById('triggerMode').value, 'and');
    // A disabled trigger is not part of the sentence.
    assert.equal(page.document.getElementById('triggerSummary').textContent,
        'Fires for messages where chat is A, and media type is audio.');

    await afterValidation(page);
    page.document.getElementById('webhookForm').dispatchEvent(new page.window.Event('submit', { cancelable: true }));
    await page.settle();

    const put = callsTo(page.calls, '/api/webhooks/5', 'PUT')[0].body;
    assert.equal(put.trigger_mode, 'and');
    assert.deepEqual(put.triggers.map((tr) => tr.enabled), [true, false, true]);
});

test('an AND listener card says all of its triggers are needed', async (t) => {
    const page = await createPage();
    t.after(page.close);

    const card = page.manager.createWebhookCard({
        id: 3, name: 'Audio in A', webhook_url: 'https://n8n.example/hook', enabled: true, trigger_mode: 'and',
        created_at: '2026-09-21T11:36:32Z',
        triggers: [
            { trigger_type: 'chat_jid', trigger_value: 'A', match_type: 'exact', enabled: true },
            { trigger_type: 'media_type', trigger_value: 'audio', match_type: 'exact', enabled: true },
        ],
    });
    assert.equal(card.querySelector('.webhook-triggers').textContent, 'All of: chat_jid: A, media_type: audio');
});

// Review bug 1: an edit made while a check is on the wire makes that check's answer stale.
// It must not be painted over the form the user is now looking at, and Save stays blocked
// until the new check answers.
test('a validate answer overtaken by an edit is not painted', async (t) => {
    const pending = [];
    const page = await createPage({
        'POST /api/webhooks/validate': () => new Promise((resolve) => pending.push(resolve)),
    });
    t.after(page.close);
    const submitBtn = page.document.getElementById('submitBtn');

    await openNewListener(page);
    setMode(page, 'and');
    setRow(page, 0, { type: 'chat_jid', value: 'A' });
    page.document.getElementById('addTriggerBtn').click();
    setRow(page, 1, { type: 'chat_jid', value: 'B' });
    await afterValidation(page);
    assert.equal(pending.length, 1, 'the first check is on the wire');

    // The user fixes row 1 before the first answer lands; then the stale answer arrives.
    setRow(page, 1, { type: 'media_type', value: 'audio' });
    pending[0]({ status: 400, body: { success: false, error: R3.message, errors: [R3] } });
    await page.settle();

    assert.equal(rows(page)[1].querySelector('.trigger-error').textContent, '', 'stale R3 was painted');
    assert.equal(submitBtn.disabled, true, 'Save must wait for the new check');

    // The new check answers clean.
    await afterValidation(page);
    assert.equal(pending.length, 2);
    pending[1]({ body: { success: true, errors: [] } });
    await page.settle();
    assert.equal(rows(page)[1].querySelector('.trigger-error').textContent, '');
    assert.equal(submitBtn.disabled, false);
});

test('an existing listener is checked on open; a new form is not', async (t) => {
    const R1 = { rule: 'R1', trigger_index: 0, message: 'R1: media type = sticker can never match.' };
    const page = await createPage({
        'POST /api/webhooks/validate': { status: 400, body: { success: false, error: R1.message, errors: [R1] } },
    });
    t.after(page.close);

    // A new form: nothing is asked, so it does not open showing "name is required".
    page.document.getElementById('addWebhookBtn').click();
    await afterValidation(page);
    assert.equal(callsTo(page.calls, '/api/webhooks/validate', 'POST').length, 0);
    assert.equal(page.document.getElementById('formErrors').hidden, true);
    assert.equal(page.document.getElementById('submitBtn').disabled, false);
    page.manager.closeModal();

    // An existing listener the rules now reject says why before anyone edits it.
    page.manager.openModal({
        id: 7, name: 'Stickers', webhook_url: 'https://n8n.example/hook', enabled: true, trigger_mode: 'or',
        triggers: [{ trigger_type: 'media_type', trigger_value: 'sticker', match_type: 'exact', enabled: true }],
    });
    await afterValidation(page);
    assert.equal(callsTo(page.calls, '/api/webhooks/validate', 'POST').length, 1);
    assert.equal(rows(page)[0].querySelector('.trigger-error').textContent, R1.message);
    assert.equal(page.document.getElementById('submitBtn').disabled, true);
});
