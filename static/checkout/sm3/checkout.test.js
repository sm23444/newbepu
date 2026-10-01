const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

class FakeElement {
    constructor() {
        this.style = {};
        this.attributes = new Map();
        this.textContent = '';
        this.className = '';
        this.children = [];
    }

    appendChild(child) {
        this.children.push(child);
        return child;
    }

    setAttribute(name, value) {
        this.attributes.set(name, String(value));
    }

    getAttribute(name) {
        return this.attributes.has(name) ? this.attributes.get(name) : null;
    }

    removeAttribute(name) {
        this.attributes.delete(name);
        if (name === 'src') this.src = '';
    }
}

function loadPayment(elements = new Map(), globals = {}) {
    const document = {
        body: new FakeElement(),
        createElement: () => new FakeElement(),
        getElementById: (id) => elements.get(id) || null,
        querySelectorAll: () => [],
        addEventListener: () => {},
        documentElement: {}
    };
    const window = {
        location: { pathname: '' },
        navigator: { language: 'zh-CN' }
    };
    const context = {
        window,
        document,
        navigator: window.navigator,
        console,
        Promise,
        setTimeout,
        clearTimeout,
        ...globals
    };
    const source = fs.readFileSync(path.join(__dirname, 'assets/js/checkout.js'), 'utf8');
    vm.runInNewContext(source, context, { filename: 'checkout.js' });
    context.Payment = context.window.Payment;
    return context.window.Payment;
}

function plain(value) {
    return JSON.parse(JSON.stringify(value));
}

test('splitWalletAddress emphasizes the first 4 and last 6 characters', () => {
    const payment = loadPayment();
    assert.deepEqual(plain(payment.splitWalletAddress('0x1234567890abcdef')), [
        { text: '0x12', emphasized: true },
        { text: '34567890', emphasized: false },
        { text: 'abcdef', emphasized: true }
    ]);
});

test('splitWalletAddress keeps short addresses intact', () => {
    const payment = loadPayment();
    assert.deepEqual(plain(payment.splitWalletAddress('1234567890')), [
        { text: '1234567890', emphasized: true }
    ]);
    assert.deepEqual(plain(payment.splitWalletAddress('')), [
        { text: '--', emphasized: false }
    ]);
});

test('updateQrPaymentLogo reveals loaded token and network logos', () => {
    const badge = new FakeElement();
    const token = new FakeElement();
    const network = new FakeElement();
    const payment = loadPayment(new Map([
        ['qrPaymentLogo', badge],
        ['qrTokenLogo', token],
        ['qrNetworkLogo', network]
    ]));

    payment.updateQrPaymentLogo('usdt', 'polygon');
    assert.equal(token.src, '/checkout/sm3/assets/web3icons/token/USDT.svg');
    assert.equal(network.src, '/checkout/sm3/assets/web3icons/network/polygon.svg');
    assert.equal(badge.style.display, 'none');
    assert.equal(network.style.display, 'none');

    token.onload();
    network.onload();
    assert.equal(badge.style.display, 'flex');
    assert.equal(network.style.display, 'block');
});

test('updateQrPaymentLogo hides images that fail to load', () => {
    const badge = new FakeElement();
    const token = new FakeElement();
    const network = new FakeElement();
    const payment = loadPayment(new Map([
        ['qrPaymentLogo', badge],
        ['qrTokenLogo', token],
        ['qrNetworkLogo', network]
    ]));

    payment.updateQrPaymentLogo('usdc', 'polygon');
    token.onerror();
    network.onerror();
    assert.equal(badge.style.display, 'none');
    assert.equal(token.src, '');
    assert.equal(network.style.display, 'none');
    assert.equal(network.src, '');
});

test('payment amount separates number and currency without changing copied precision', async () => {
    const elements = new Map([
        'selectionStage', 'paymentStage', 'orderAmountQ', 'payAmountNumberQ',
        'payAmountCurrencyQ', 'orderIdQ', 'walletAddress', 'cmusToast'
    ].map((id) => [id, new FakeElement()]));
    const copied = [];
    let onReady;
    let onCopy;
    elements.set('copyAmountQBtn', {
        addEventListener: (event, handler) => { if (event === 'click') onCopy = handler; }
    });
    const order = {
        status: 1,
        trade_type: 'usdt.bsc',
        token: '0x1234567890abcdef',
        actual_amount: '48.636400',
        network: { crypto: 'USDT', name: 'Bsc', key: 'bsc' },
        money: '321',
        fiat: 'CNY',
        order_id: 'PAY20261001110952',
        expired_at: Math.floor(Date.now() / 1000) + 3600
    };
    const window = {
        location: { pathname: '/pay/amount-test' },
        navigator: {
            language: 'zh-CN',
            clipboard: { writeText: async (text) => { copied.push(text); } }
        }
    };
    loadPayment(elements, {
        window,
        navigator: window.navigator,
        document: {
            getElementById: (id) => elements.get(id) || null,
            createElement: () => new FakeElement(),
            addEventListener: (event, handler) => { if (event === 'DOMContentLoaded') onReady = handler; }
        },
        fetch: async () => ({ json: async () => ({ status_code: 200, data: order }) }),
        $: () => ({ empty: () => {}, qrcode: () => {} }),
        setInterval: () => 1,
        clearInterval: () => {},
        setTimeout: () => 1
    });
    onReady();
    await new Promise(setImmediate);

    assert.equal(elements.get('paymentStage').style.display, 'block');
    assert.equal(elements.get('payAmountNumberQ').textContent, '48.636400');
    assert.equal(elements.get('payAmountCurrencyQ').textContent, 'USDT');
    assert.equal(window._qrAmount, '48.636400');
    assert.equal(elements.get('cmusToast').textContent, '');
    assert.equal(typeof onCopy, 'function');

    elements.delete('cmusToast');
    onCopy();
    await new Promise(setImmediate);
    assert.deepEqual(copied, ['48.636400']);
});

test('payment amount markup retains separate number and currency elements', () => {
    const html = fs.readFileSync(path.join(__dirname, 'views/checkout.html'), 'utf8');
    assert.match(html, /id="payAmountQ"><span class="amount-crypto-number" id="payAmountNumberQ"><\/span> <span class="amount-crypto-currency" id="payAmountCurrencyQ"><\/span><\/span>/);
});

test('checkout script cache key matches its content hash', () => {
    const html = fs.readFileSync(path.join(__dirname, 'views/checkout.html'), 'utf8');
    const script = fs.readFileSync(path.join(__dirname, 'assets/js/checkout.js'), 'utf8').replace(/\r\n/g, '\n');
    const contentHash = crypto.createHash('sha256').update(script, 'utf8').digest('hex').slice(0, 12);

    assert.match(html, new RegExp('checkout\\.js\\?v=' + contentHash));
});

test('checkout stylesheet cache key matches its content hash', () => {
    const html = fs.readFileSync(path.join(__dirname, 'views/checkout.html'), 'utf8');
    const stylesheet = fs.readFileSync(path.join(__dirname, 'assets/css/checkout.css'), 'utf8').replace(/\r\n/g, '\n');
    const contentHash = crypto.createHash('sha256').update(stylesheet, 'utf8').digest('hex').slice(0, 12);

    assert.match(html, new RegExp('checkout\\.css\\?v=' + contentHash));
});

test('sm3 metadata and asset references are isolated from sm2', () => {
    const info = JSON.parse(fs.readFileSync(path.join(__dirname, 'checkout.json'), 'utf8'));
    assert.equal(info.name, 'sm3');
    for (const file of ['views/checkout.html', 'assets/js/checkout.js']) {
        const source = fs.readFileSync(path.join(__dirname, file), 'utf8');
        assert.doesNotMatch(source, /\/checkout\/sm2\//);
        assert.match(source, /\/checkout\/sm3\/assets\//);
    }
    const html = fs.readFileSync(path.join(__dirname, 'views/checkout.html'), 'utf8');
    for (const match of html.matchAll(/(?:src|href)="\/checkout\/sm3\/([^"?]+)(?:\?[^"]*)?"/g)) {
        assert.ok(fs.existsSync(path.join(__dirname, match[1])), match[1]);
    }
});

test('sm3 option builder groups the fixed icon frame and left-aligned text', () => {
    const source = fs.readFileSync(path.join(__dirname, 'assets/js/checkout.js'), 'utf8');
    const builder = source.match(/    function buildOptionCard\(options\) \{[\s\S]*?(?=\n    function preloadIcons)/)[0];
    const document = {
        createElement: () => Object.assign(new FakeElement(), { addEventListener() {} })
    };
    const card = vm.runInNewContext(builder + '\nbuildOptionCard(options);', {
        document,
        SVG_K: '<svg width="W"></svg>',
        options: { name: 'TRON', detail: '波场网络', iconSrc: '/tron.svg', selected: true, onClick() {} }
    });
    assert.equal(card.getAttribute('aria-pressed'), 'true');
    assert.equal(card.children[1].className, 'payment-option-icon-frame');
    assert.equal(card.children[1].children[0].src, '/tron.svg');
    assert.equal(card.children[2].className, 'payment-option-text');
    assert.deepEqual(Array.from(card.children[2].children, (el) => el.textContent), ['TRON', '波场网络']);
});
