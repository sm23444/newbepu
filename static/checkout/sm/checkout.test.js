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

function loadPayment(elements = new Map()) {
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
        clearTimeout
    };
    const source = fs.readFileSync(path.join(__dirname, 'assets/js/checkout.js'), 'utf8');
    vm.runInNewContext(source, context, { filename: 'checkout.js' });
    return window.Payment;
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
    assert.equal(token.src, '/checkout/sm/assets/web3icons/token/USDT.svg');
    assert.equal(network.src, '/checkout/sm/assets/web3icons/network/polygon.svg');
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

test('manual review requires and submits a transaction reference', () => {
    const html = fs.readFileSync(path.join(__dirname, 'views/checkout.html'), 'utf8');
    const script = fs.readFileSync(path.join(__dirname, 'assets/js/checkout.js'), 'utf8');

    assert.match(html, /id="paymentReviewTransactionHash"[^>]*required/);
    assert.match(script, /body\.append\('transaction_hash', transactionHash\.value\.trim\(\)\)/);
});

test('checkout script cache key matches its content hash', () => {
    const html = fs.readFileSync(path.join(__dirname, 'views/checkout.html'), 'utf8');
    const script = fs.readFileSync(path.join(__dirname, 'assets/js/checkout.js'), 'utf8').replace(/\r\n/g, '\n');
    const contentHash = crypto.createHash('sha256').update(script, 'utf8').digest('hex').slice(0, 12);

    assert.match(html, new RegExp('checkout\\.js\\?v=' + contentHash));
});

test('manual review availability matches backend reviewable order states', () => {
    const payment = loadPayment();

    assert.deepEqual([1, 2, 3, 4, 5, 6].map(status => payment.paymentReviewAvailable(status)), [
        true,
        false,
        true,
        false,
        true,
        true
    ]);
});
