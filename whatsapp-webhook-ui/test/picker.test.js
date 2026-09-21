'use strict';

const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createPage, callsTo } = require('./harness');

const chats = [
    { jid: '244332250583270@lid', name: 'DUDI COHEN', phone: '972527872233', is_group: false },
    { jid: '120363041467831150@g.us', name: 'Israelis @ Kubecon', is_group: true },
    // Nobody ever named this person: the bridge falls back to their number.
    { jid: '111111111111111@lid', name: '972500000001', phone: '972500000001', is_group: false },
];

function triggerRow(page) {
    const row = page.document.querySelector('.trigger-item');
    return {
        row,
        type: row.querySelector('.trigger-type'),
        value: row.querySelector('.trigger-value'),
    };
}

async function chooseType(page, type) {
    const { type: select } = triggerRow(page);
    select.value = type;
    select.dispatchEvent(new page.window.Event('change'));
    await page.settle();
}

test('a chat trigger offers chats by name, with the number beside a person', async (t) => {
    const page = await createPage({ '/api/chats': { body: { success: true, data: chats } } });
    t.after(page.close);

    await chooseType(page, 'chat_jid');

    const { value, row } = triggerRow(page);
    const datalist = row.querySelector('datalist.chat-options');
    assert.ok(datalist, 'a datalist should be attached to the row');
    assert.equal(value.getAttribute('list'), datalist.id);

    const options = [...datalist.querySelectorAll('option')].map((o) => [o.value, o.textContent]);
    assert.deepEqual(options, [
        // Picking writes the JID; the label is what a person reads.
        ['244332250583270@lid', 'DUDI COHEN · 972527872233'],
        ['120363041467831150@g.us', 'Israelis @ Kubecon (group)'],
        // A name that is only the number is not repeated.
        ['111111111111111@lid', '972500000001'],
    ]);
});

test('a sender trigger asks for 1:1 chats only', async (t) => {
    const page = await createPage();
    t.after(page.close);

    await chooseType(page, 'sender');

    const lookups = callsTo(page.calls, '/api/chats');
    assert.equal(lookups.length, 1);
    assert.equal(lookups[0].url.searchParams.get('type'), 'contact');
    assert.match(triggerRow(page).value.placeholder, /phone number/);
});

test('typing searches the bridge, debounced', async (t) => {
    const page = await createPage();
    t.after(page.close);
    await chooseType(page, 'chat_jid');

    const { value } = triggerRow(page);
    for (const text of ['9', '97', '972527872233']) {
        value.value = text;
        value.dispatchEvent(new page.window.Event('input'));
    }
    await new Promise((resolve) => page.window.setTimeout(resolve, 350));
    await page.settle();

    const searches = callsTo(page.calls, '/api/chats').filter((c) => c.url.searchParams.has('q'));
    // Three keystrokes inside the debounce window make one request, for the final text.
    assert.equal(searches.length, 1);
    assert.equal(searches[0].url.searchParams.get('q'), '972527872233');
});

test('a phone number can still be typed by hand', async (t) => {
    // The picker must never block free text: a number is a valid value, and the bridge
    // expands it to the @lid chat the person actually writes from.
    const page = await createPage({ '/api/chats': { status: 500, body: { success: false } } });
    t.after(page.close);
    await chooseType(page, 'sender');

    const { value } = triggerRow(page);
    assert.equal(value.disabled, false);
    value.value = '972527872233';
    assert.equal(page.manager.collectTriggers()[0].trigger_value, '972527872233');
});

test('switching back to All messages disables the value and drops the list', async (t) => {
    const page = await createPage();
    t.after(page.close);
    await chooseType(page, 'chat_jid');
    await chooseType(page, 'all');

    const { value } = triggerRow(page);
    assert.equal(value.disabled, true);
    assert.equal(value.hasAttribute('list'), false);
});
