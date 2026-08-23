(function () {
    'use strict';

    var i18nReady = false;
    var lang = 'zh';
    var SVG_C = '<svg width="W" height="W" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>';
    var SVG_K = '<svg width="W" height="W" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>';
    var WEB3 = '/checkout/sm/assets/web3icons';
    var iconCache = {};
    var methods = [], networkSort = '';
    var selCur = '', selMethod = null;
    var cfg = {}, tradeId = '';
    var cdTimer = null, stTimer = null;

    function detectLang() {
        try {
            return ((navigator.language || navigator.userLanguage || 'en').toLowerCase().indexOf('zh') === 0) ? 'zh' : 'en';
        } catch (e) { return 'en'; }
    }

    function initI18n() {
        lang = detectLang();
        return new Promise(function (resolve) {
            if (typeof i18next === 'undefined') return resolve();
            i18next.init({ lng: lang, debug: false, resources: {} }, function (err) {
                if (err) return resolve();
                fetch('/checkout/sm/assets/locales/' + lang + '.json?v=sm-20260727-3')
                    .then(function (r) { return r.json(); })
                    .then(function (d) {
                        i18next.addResourceBundle(lang, 'translation', d);
                        i18nReady = true;
                        applyI18n();
                        resolve();
                    })
                    .catch(function () { resolve(); });
            });
        });
    }

    function t(key, def) {
        if (i18nReady && typeof i18next !== 'undefined') {
            var v = i18next.t(key);
            if (v && v !== key) return v;
        }
        return def != null ? def : key;
    }

    function applyI18n() {
        if (!i18nReady || typeof i18next === 'undefined') return;
        document.querySelectorAll('[data-i18n]').forEach(function (el) {
            var k = el.getAttribute('data-i18n');
            if (!k) return;
            if (k.charAt(0) === '[') {
                var m = k.match(/\[(.+?)\](.+)/);
                if (!m) return;
                var v = i18next.t(m[2]);
                if (m[1] === 'html') el.innerHTML = v;
                else el.setAttribute(m[1], v);
                return;
            }
            var tr = i18next.t(k);
            if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') el.placeholder = tr;
            else el.innerHTML = tr;
        });
        try {
            document.title = i18next.t('pageTitle');
            document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
        } catch (e) {}
    }

    function switchLang(l) {
        if (l !== 'zh' && l !== 'en') { console.warn('Use "zh" or "en"'); return; }
        if (typeof i18next === 'undefined') return;
        lang = l;
        fetch('/checkout/sm/assets/locales/' + l + '.json?v=sm-20260727-3')
            .then(function (r) { return r.json(); })
            .then(function (d) {
                i18next.addResourceBundle(l, 'translation', d, true, true);
                return i18next.changeLanguage(l);
            })
            .then(function () { applyI18n(); console.log('✓ Language: ' + l); })
            .catch(function (e) { console.error(e); });
    }

    function showToast(msg, type) {
        var el = document.getElementById('cmusToast');
        if (!el) return;
        el.textContent = msg;
        el.className = 'toast ' + (type || '');
        el.classList.add('show');
        clearTimeout(el._t);
        el._t = setTimeout(function () { el.classList.remove('show'); }, 3000);
    }

    function copyText(text, msg, iconEl, sm, labelEl) {
        if (!text) return;
        var sz = sm ? 14 : 16;
        var ok = function () {
            showToast(msg || t('toastCopied'), 'success');
            if (iconEl) {
                iconEl.innerHTML = SVG_K.replace(/W/g, sz);
                iconEl.classList.add('copied');
                clearTimeout(iconEl._r);
                iconEl._r = setTimeout(function () {
                    iconEl.innerHTML = SVG_C.replace(/W/g, sz);
                    iconEl.classList.remove('copied');
                }, 1500);
            }
            if (labelEl) {
                labelEl.textContent = '已复制';
                clearTimeout(labelEl._r);
                labelEl._r = setTimeout(function () {
                    labelEl.textContent = labelEl.dataset.defaultLabel || '复制UID';
                }, 1500);
            }
        };
        var fb = function () {
            var ta = document.createElement('textarea');
            ta.value = text;
            ta.style.cssText = 'position:fixed;opacity:0;top:0;left:0;';
            document.body.appendChild(ta);
            ta.select();
            try { document.execCommand('copy'); ok(); }
            catch (e) { showToast(t('toastCopyFailed', '复制失败'), 'error'); }
            document.body.removeChild(ta);
        };
        if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(text).then(ok).catch(fb);
        } else fb();
    }

    function tokenIcon(c) { return WEB3 + '/token/' + (c || '').toUpperCase() + '.svg'; }
    function netIcon(n) {
        var key = (n || '').toLowerCase();
        if (key === 'binance') return WEB3 + '/network/binance.png?v=sm-20260724-3';
        if (key === 'okx') return WEB3 + '/network/okx.png?v=sm-20260724-3';
        return WEB3 + '/network/' + key + '.svg';
    }

    function splitWalletAddress(value) {
        var address = value == null || value === '' ? '--' : String(value);
        if (address === '--') return [{ text: address, emphasized: false }];
        if (address.length <= 10) return [{ text: address, emphasized: true }];
        return [
            { text: address.slice(0, 4), emphasized: true },
            { text: address.slice(4, -6), emphasized: false },
            { text: address.slice(-6), emphasized: true }
        ];
    }

    function updateQrPaymentLogo(currencyValue, networkValue) {
        var badge = document.getElementById('qrPaymentLogo');
        var tokenLogo = document.getElementById('qrTokenLogo');
        var networkLogo = document.getElementById('qrNetworkLogo');
        if (!badge || !tokenLogo || !networkLogo) return;

        var currency = currencyValue == null ? '' : String(currencyValue).trim().toUpperCase();
        var network = networkValue == null ? '' : String(networkValue).trim().toLowerCase();
        badge.style.display = 'none';
        tokenLogo.removeAttribute('src');
        networkLogo.style.display = 'none';
        networkLogo.removeAttribute('src');
        tokenLogo.onload = null;
        tokenLogo.onerror = null;
        networkLogo.onload = null;
        networkLogo.onerror = null;
        if (!currency) return;

        tokenLogo.onerror = function () {
            badge.style.display = 'none';
            tokenLogo.removeAttribute('src');
        };
        tokenLogo.onload = function () {
            badge.style.display = 'flex';
        };
        tokenLogo.src = tokenIcon(currency);

        if (!network) return;
        networkLogo.onerror = function () {
            networkLogo.style.display = 'none';
            networkLogo.removeAttribute('src');
        };
        networkLogo.onload = function () {
            networkLogo.style.display = 'block';
        };
        networkLogo.src = netIcon(network);
    }

    function paymentReviewAvailable(status) {
        status = Number(status);
        return status === 1 || status === 3 || status === 5 || status === 6;
    }

    function networkName(network) {
        var names = {
            arbitrum: 'ARBITRUM',
            aptos: 'APTOS',
            base: 'BASE',
            bsc: 'BSC',
            ethereum: 'ETHEREUM',
            plasma: 'PLASMA',
            polygon: 'POLYGON',
            solana: 'SOLANA',
            ton: 'TON',
            tron: 'TRON',
            xlayer: 'X LAYER'
        };
        var key = (network || '').toLowerCase();
        return names[key] || key.replace(/[-_]+/g, ' ').toUpperCase();
    }

    function buildOptionCard(options) {
        var button = document.createElement('button');
        button.type = 'button';
        button.className = 'payment-option-card' + (options.selected ? ' selected' : '');
        button.setAttribute('aria-pressed', options.selected ? 'true' : 'false');
        button.setAttribute('aria-label', options.ariaLabel || options.name);

        var check = document.createElement('span');
        check.className = 'payment-option-check';
        check.setAttribute('aria-hidden', 'true');
        check.innerHTML = SVG_K.replace(/W/g, 12);

        var icon = document.createElement('img');
        icon.className = 'payment-option-icon' + (options.iconClass ? ' ' + options.iconClass : '');
        icon.src = options.iconSrc;
        icon.alt = '';
        icon.addEventListener('error', function () { icon.classList.add('missing'); });

        var name = document.createElement('span');
        name.className = 'payment-option-name';
        name.textContent = options.name;

        button.appendChild(check);
        button.appendChild(icon);
        button.appendChild(name);

        if (options.detail) {
            var detail = document.createElement('span');
            detail.className = 'payment-option-detail';
            detail.textContent = options.detail;
            button.appendChild(detail);
        }
        if (options.badge) {
            var badge = document.createElement('span');
            badge.className = 'payment-option-badge';
            badge.textContent = options.badge;
            button.appendChild(badge);
        }

        button.addEventListener('click', options.onClick);
        return button;
    }

    function preloadIcons(srcs, cb) {
        var pending = srcs.length;
        if (!pending) return cb();
        srcs.forEach(function (src) {
            if (iconCache[src]) { if (--pending === 0) cb(); return; }
            fetch(src)
                .then(function (r) { return r.ok ? r.blob() : null; })
                .then(function (blob) {
                    if (blob) {
                        var reader = new FileReader();
                        reader.onload = function () { iconCache[src] = reader.result; if (--pending === 0) cb(); };
                        reader.readAsDataURL(blob);
                    } else { iconCache[src] = src; if (--pending === 0) cb(); }
                })
                .catch(function () { iconCache[src] = src; if (--pending === 0) cb(); });
        });
    }
    function cached(src) { return iconCache[src] || src; }

    function sortMethodsByNetwork(list, sortValue) {
        if (!Array.isArray(list) || typeof sortValue !== 'string' || !sortValue.trim()) return list;

        var sortIndex = Object.create(null);
        var rank = 0;
        sortValue.split(',').forEach(function (network) {
            var key = network.trim().toLowerCase();
            if (!key || Object.prototype.hasOwnProperty.call(sortIndex, key)) return;
            sortIndex[key] = rank++;
        });
        if (!rank) return list;

        return list.map(function (method, index) {
            var network = method && typeof method.network === 'string' ? method.network.trim().toLowerCase() : '';
            var configured = Object.prototype.hasOwnProperty.call(sortIndex, network);
            return {
                method: method,
                index: index,
                configured: configured,
                rank: configured ? sortIndex[network] : -1
            };
        }).sort(function (a, b) {
            if (a.configured !== b.configured) return a.configured ? -1 : 1;
            if (a.configured && a.rank !== b.rank) return a.rank - b.rank;
            return a.index - b.index;
        }).map(function (item) { return item.method; });
    }

    function renderNetworkTag(el, label, network) {
        if (!el) return;
        el.textContent = '';
        if (!label) return;

        var wrap = document.createElement('span');
        wrap.className = 'network-tag-name';

        var img = document.createElement('img');
        var iconKey = String(network || label || '').toLowerCase().replace(/[^a-z0-9]+/g, '-');
        img.className = 'network-tag-icon network-icon-' + iconKey;
        img.alt = '';
        img.src = cached(netIcon(network || label));
        img.onerror = function () { img.style.display = 'none'; };

        var text = document.createElement('span');
        text.textContent = label;

        wrap.appendChild(img);
        wrap.appendChild(text);
        el.appendChild(wrap);
    }
    window.renderNetworkTag = renderNetworkTag;

    function initSelectionUI() {
        if (!methods.length) return;
        var currencies = Array.from(new Set(methods.map(function (m) { return m.currency; })));
        currencies.sort(function (a, b) {
            if (a === 'USDT') return -1;
            if (b === 'USDT') return 1;
            return 0;
        });
        var srcs = currencies.map(tokenIcon).concat(methods.map(function (m) { return netIcon(m.network); }));
        preloadIcons(srcs, function () {
            selCur = '';
            selMethod = null;
            renderCurrencyCards(currencies);
            renderNetworkCards();
            updateAmount();
            updatePayBtn();
        });
    }

    function renderCurrencyCards(currencies) {
        var grid = document.getElementById('currencyGrid');
        if (!grid) return;
        grid.textContent = '';
        currencies.forEach(function (currency) {
            grid.appendChild(buildOptionCard({
                name: currency,
                iconSrc: cached(tokenIcon(currency)),
                selected: currency === selCur,
                ariaLabel: t('currencyLabel', '货币') + ' ' + currency,
                onClick: function () {
                    if (selCur === currency) return;
                    selCur = currency;
                    selMethod = null;
                    renderCurrencyCards(currencies);
                    renderNetworkCards();
                    updateAmount();
                    updatePayBtn();
                }
            }));
        });
    }

    function renderNetworkCards() {
        var grid = document.getElementById('networkGrid');
        if (!grid) return;
        grid.textContent = '';
        var label = document.getElementById('networkSectionLabel');
        if (label) label.style.display = selCur ? '' : 'none';
        grid.style.display = selCur ? '' : 'none';
        if (!selCur) return;
        var available = sortMethodsByNetwork(methods.filter(function (m) { return m.currency === selCur; }), networkSort);
        if (!available.length) {
            var empty = document.createElement('div');
            empty.className = 'payment-options-empty';
            empty.textContent = t('toastLoadFailed', '暂无可用付款网络');
            grid.appendChild(empty);
            return;
        }
        available.forEach(function (method) {
            var protocol = (method.token_custom_name || method.token_net_name || '').toUpperCase();
            var name = networkName(method.network);
            grid.appendChild(buildOptionCard({
                name: name,
                detail: protocol && protocol !== name ? protocol : '',
                iconSrc: cached(netIcon(method.network)),
                iconClass: 'network-icon-' + String(method.network || '').toLowerCase().replace(/[^a-z0-9]+/g, '-'),
                badge: method.is_popular ? t('hotBadge', '热门') : '',
                selected: method === selMethod,
                ariaLabel: t('networkLabel', '网络') + ' ' + name + (protocol ? ' ' + protocol : ''),
                onClick: function () {
                    selMethod = method;
                    renderNetworkCards();
                    updateAmount();
                    updatePayBtn();
                }
            }));
        });
    }

    function updateAmount() {
        var aEl = document.getElementById('payAmountCrypto');
        var nEl = document.getElementById('payNetworkTag');
        var lineEl = document.getElementById('networkTagSep');
        var rowEl = aEl ? aEl.closest('.amount-crypto-row') : null;
        if (!aEl) return;
        if (selMethod) {
            aEl.textContent = selMethod.actual_amount + ' ' + selMethod.currency;
            if (window.renderNetworkTag) {
                window.renderNetworkTag(nEl, selMethod.token_net_name, selMethod.network);
            } else if (nEl) {
                nEl.textContent = selMethod.token_net_name;
            }
            if (lineEl) lineEl.style.display = 'flex';
            if (rowEl) rowEl.style.display = '';
        } else {
            aEl.textContent = '--';
            if (nEl) nEl.textContent = '';
            if (lineEl) lineEl.style.display = 'none';
            if (rowEl) rowEl.style.display = 'none';
        }
    }

    function updatePayBtn() {
        var b = document.getElementById('payBtn');
        if (b) b.disabled = !selMethod;
    }

    function startStatusCheck() {
        if (stTimer) clearInterval(stTimer);
        stTimer = setInterval(checkStatus, 5000);
        checkStatus();
    }

    function checkStatus() {
        if (!tradeId) return;
        fetch('/api/v1/pay/info', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ trade_id: tradeId })
        })
            .then(function (r) { return r.json(); })
            .then(function (res) {
                if (res.status_code !== 200) return;
                var d = res.data;
                if (d.status === 5) showConfirming();
                else if (d.status === 2) {
                    stopTimers();
                    hideConfirming();
                    showSuccess(d);
                } else if (d.status === 4) {
                    showCanceled(d);
                }
            })
            .catch(function (e) { console.error(e); });
    }

    function stopTimers() {
        if (stTimer) {
            clearInterval(stTimer);
            stTimer = null;
        }
        if (cdTimer) {
            clearInterval(cdTimer);
            cdTimer = null;
        }
    }

    function hideCheckoutStages() {
        var selection = document.getElementById('selectionStage');
        var payment = document.getElementById('paymentStage');
        if (selection) selection.style.display = 'none';
        if (payment) payment.style.display = 'none';
    }

    function startCountdown(timerEl, expiredAt, createdAt, onExpire) {
        if (cdTimer) clearInterval(cdTimer);
        var total = expiredAt - createdAt;
        if (total <= 0) total = 1;
        var wrap = timerEl ? timerEl.closest('.timer-badge-wrap') : null;
        var bar = wrap ? wrap.querySelector('.timer-ring-bar') : null;

        function tick() {
            var rem = Math.max(0, expiredAt - Math.floor(Date.now() / 1000));
            var hh = Math.floor(rem / 3600);
            var mm = Math.floor((rem % 3600) / 60);
            var ss = rem % 60;
            if (timerEl) timerEl.textContent = ('0' + hh).slice(-2) + ':' + ('0' + mm).slice(-2) + ':' + ('0' + ss).slice(-2);
            var pct = rem / total;
            if (bar) {
                bar.style.strokeDashoffset = (1 - pct) * 100;
                bar.classList.toggle('danger', pct <= 0.10);
                bar.classList.toggle('warn', pct > 0.10 && pct <= 0.30);
            }
            if (timerEl) {
                timerEl.classList.toggle('danger', pct <= 0.10);
                timerEl.classList.toggle('warn', pct > 0.10 && pct <= 0.30);
            }
            if (rem <= 0) {
                clearInterval(cdTimer);
                if (onExpire) onExpire();
            }
        }
        tick();
        cdTimer = setInterval(tick, 1000);
    }

    function showConfirming() {
        var m = document.getElementById('confirmingModal');
        if (m && m.style.display === 'none') m.style.display = 'flex';
    }
    function hideConfirming() {
        var m = document.getElementById('confirmingModal');
        if (m) m.style.display = 'none';
    }

    function safeHttpsUrl(value, fallback) {
        var raw = String(value || '').trim();
        if (!raw) return fallback || '';
        try {
            var parsed = new URL(raw);
            if (parsed.protocol !== 'https:' || !parsed.host) return fallback || '';
            return parsed.href;
        } catch (e) {
            return fallback || '';
        }
    }

    function showSuccess(data) {
        var modal = document.getElementById('successModal');
        if (!modal) return;
        modal.style.display = 'flex';
        if (data.trade_url) {
            var sec = document.getElementById('txHashSection');
            if (sec) sec.style.display = 'block';
            var link = document.getElementById('txHashLink');
            if (link) link.href = data.trade_url;
            var disp = document.getElementById('txHashDisplayText');
            if (disp) {
                var u = data.trade_url;
                disp.textContent = u.length > 40 ? u.slice(0, 20) + '...' + u.slice(-16) : u;
            }
        }
        var ret = safeHttpsUrl(data.redirect_url || (cfg && cfg.return_url), '');
        var btn = document.getElementById('returnBtn');
        if (btn) {
            if (ret) {
                btn.href = ret;
                btn.style.display = '';
            }
            else btn.style.display = 'none';
        }
        showToast(t('paymentSuccessToast', '支付成功'), 'success');
    }

    function showCanceled(data) {
        stopTimers();
        hideConfirming();
        hideCheckoutStages();
        var ret = (data && data.redirect_url) || (cfg && cfg.return_url) || '/';
        var modal = document.getElementById('canceledModal');
        if (!modal) {
            modal = document.createElement('div');
            modal.id = 'canceledModal';
            modal.className = 'modal-overlay';
            modal.innerHTML =
                '<div class="modal-card"><div class="modal-body">' +
                '<div style="margin-bottom:16px;display:flex;justify-content:center;">' +
                    '<svg width="64" height="64" viewBox="0 0 24 24" fill="none" stroke="#64748b" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
                        '<circle cx="12" cy="12" r="10"/>' +
                        '<path d="M15 9l-6 6"/>' +
                        '<path d="M9 9l6 6"/>' +
                    '</svg>' +
                '</div>' +
                '<div class="modal-title" style="color:#475569;">' + t('canceledTitle', '订单已取消') + '</div>' +
                '<p class="modal-subtitle">' + t('canceledMessage', '该订单已取消，不能继续付款。<br>如需支付，请重新发起订单。') + '</p>' +
                '<a href="/" class="return-btn">' + t('returnBtn', '返回商户平台') + '</a>' +
                '</div></div>';
            document.body.appendChild(modal);
        }
        var canceledReturn = modal.querySelector('.return-btn');
        if (canceledReturn) canceledReturn.href = safeHttpsUrl(ret, '/');
        modal.style.display = 'flex';
        showToast(t('orderCanceledToast', '订单已取消'), 'error');
    }

    function showTimeout() {
        if (document.getElementById('timeoutModal')) return;
        var ret = cfg.return_url || '/';
        var ov = document.createElement('div');
        ov.id = 'timeoutModal';
        ov.className = 'modal-overlay';
        ov.innerHTML =
            '<div class="modal-card timeout-card"><div class="modal-body timeout-modal-body">' +
            '<div class="timeout-icon">' +
                '<svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
                    '<circle cx="12" cy="12" r="10"/>' +
                    '<polyline points="12 6 12 12 16 14"/>' +
                '</svg>' +
            '</div>' +
            '<div class="modal-title timeout-title">' + t('timeoutTitle', '支付已超时') + '</div>' +
            '<p class="modal-subtitle">' + t('timeoutMessage', '很抱歉，支付时间已超时。<br>如已付款，可申请人工复核。') + '</p>' +
            '<div class="timeout-actions">' +
                '<button type="button" class="timeout-review-btn" id="timeoutReviewButton">已付款？申请人工复核</button>' +
                '<a href="/" class="timeout-return-link">' + t('returnBtn', '返回商户平台') + '</a>' +
            '</div>' +
            '</div></div>';
        document.body.appendChild(ov);
        var timeoutReturn = ov.querySelector('.timeout-return-link');
        if (timeoutReturn) timeoutReturn.href = safeHttpsUrl(ret, '/');
        var timeoutReview = document.getElementById('timeoutReviewButton');
        if (timeoutReview) timeoutReview.addEventListener('click', function () {
            if (window.openPaymentReview) window.openPaymentReview();
        });
    }

    function createTransaction() {
        if (cfg && cfg.status && cfg.status !== 1) {
            showToast(t('toastOrderNotPayable', '当前订单状态不允许继续付款'), 'error');
            return;
        }
        if (!selMethod) return;
        fetch('/api/v1/pay/update-order', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ trade_id: tradeId, currency: selMethod.currency, network: selMethod.network })
        })
            .then(function (r) { return r.json(); })
            .then(function (res) {
                if (res.status_code === 200 && res.data && res.data.payment_url) {
                    window.location.href = res.data.payment_url;
                } else showToast(res.message || t('toastCreateFailed', '创建交易失败'), 'error');
            })
            .catch(function () { showToast(t('toastNetworkError', '网络错误'), 'error'); });
    }

    window.Payment = {
        init: function (config) {
            cfg = config || {};
            cfg.return_url = safeHttpsUrl(cfg.return_url, '');
            tradeId = cfg.trade_id;
            startCountdown(document.getElementById('timerDisplay'), parseInt(cfg.expired_at) || 0, parseInt(cfg.created_at) || 0, showTimeout);
            startStatusCheck();
            var payBtn = document.getElementById('payBtn');
            if (payBtn && !payBtn.dataset.bound) {
                payBtn.dataset.bound = '1';
                payBtn.addEventListener('click', createTransaction);
            }
            var caBtn = document.getElementById('copyAmountBtn');
            var caIcon = document.getElementById('copyAmountIcon');
            if (caBtn) caBtn.addEventListener('click', function () {
                if (selMethod) copyText(selMethod.actual_amount, t('toastAmountCopied', '金额已复制'), caIcon, false);
            });
            fetch('/api/v1/pay/methods', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ trade_id: tradeId })
            })
                .then(function (r) { return r.json(); })
                .then(function (res) {
                    if (res.status_code === 200 && res.data && Array.isArray(res.data.methods) && res.data.methods.length) {
                        methods = res.data.methods;
                        networkSort = typeof res.data.network_sort === 'string' ? res.data.network_sort : '';
                        initSelectionUI();
                    } else {
                        methods = [];
                        networkSort = '';
                        showToast(res.message || t('toastLoadFailed', '无法加载付款网络'), 'error');
                    }
                })
                .catch(function () { showToast(t('toastNetworkError', '网络错误'), 'error'); });
        },
        initQrPage: function (config) {
            cfg = config || {};
            cfg.return_url = safeHttpsUrl(cfg.return_url, '');
            tradeId = cfg.trade_id;
            startCountdown(document.getElementById('timerDisplayQ'), parseInt(cfg.expired_at) || 0, parseInt(cfg.created_at) || 0, cfg.manual_tx_submit ? null : showTimeout);
            startStatusCheck();
            var caBtn = document.getElementById('copyAmountQBtn');
            var caIcon = document.getElementById('copyAmountQIcon');
            if (caBtn) caBtn.addEventListener('click', function () {
                if (window._qrAmount) copyText(window._qrAmount, t('toastAmountCopied', '金额已复制'), caIcon, false);
            });
            var adBtn = document.getElementById('copyAddressBtn');
            var adIcon = document.getElementById('copyAddressIcon');
            if (adBtn) adBtn.addEventListener('click', function () {
                var el = document.getElementById('walletAddress');
                var hint = document.getElementById('copyAddressHint');
                if (el && el.textContent !== '--') copyText(el.textContent, t('toastAddressCopied', '地址已复制'), adIcon, true, hint);
            });
        },
        initI18n: initI18n,
        applyI18n: applyI18n,
        t: t,
        safeHttpsUrl: safeHttpsUrl,
        splitWalletAddress: splitWalletAddress,
        updateQrPaymentLogo: updateQrPaymentLogo,
        paymentReviewAvailable: paymentReviewAvailable,
        showCanceled: showCanceled,
        switchLang: switchLang
    };
})();

(function () {
    var orderData = null, gTradeId = '';
    window._qrAmount = '';

    function _t(k, d) { return (window.Payment && window.Payment.t) ? window.Payment.t(k, d) : d; }

    function safeHttpsUrl(value, fallback) {
        if (window.Payment && typeof window.Payment.safeHttpsUrl === 'function') {
            return window.Payment.safeHttpsUrl(value, fallback);
        }
        return fallback || '';
    }

    function renderWalletAddress(value) {
        var el = document.getElementById('walletAddress');
        if (!el) return;

        el.textContent = '';
        var parts = window.Payment && typeof window.Payment.splitWalletAddress === 'function'
            ? window.Payment.splitWalletAddress(value)
            : [{ text: value == null || value === '' ? '--' : String(value), emphasized: false }];
        parts.forEach(function (item) {
            var part = document.createElement('span');
            part.textContent = item.text;
            if (item.emphasized) part.className = 'address-emphasis';
            el.appendChild(part);
        });
    }

    function paymentNotice(network) {
        return _t('paymentNotice', '请仅通过 {{network}} 网络转账，其他网络无法到账，并严格按照上方应付金额支付')
            .replace('{{network}}', network || '--');
    }

    function renderPaymentNotice(el, provider, network) {
        if (provider !== 'okx' && provider !== 'binance') {
            el.textContent = paymentNotice(network);
            return;
        }

        el.textContent = '';
        var exchangeName = provider === 'okx' ? '欧易' : '币安';
        var transferLabel = provider === 'okx' ? '内部UID转账功能' : '币安站内ID转账功能';
        var accountLabel = provider === 'okx' ? 'UID' : 'ID';
        el.appendChild(document.createTextNode('请使用' + exchangeName + ' App 的'));
        var highlight = document.createElement('span');
        highlight.className = 'notice-highlight';
        highlight.textContent = transferLabel;
        el.appendChild(highlight);
        el.appendChild(document.createTextNode('，向下方' + accountLabel + '付款。请勿使用链上提币，并严格按照'));
        var amountHighlight = document.createElement('span');
        amountHighlight.className = 'notice-highlight';
        amountHighlight.textContent = '应付金额';
        el.appendChild(amountHighlight);
        el.appendChild(document.createTextNode('支付。'));
    }

    function showSelection() {
        document.getElementById('selectionStage').style.display = 'block';
        document.getElementById('paymentStage').style.display = 'none';
        var d = orderData;
        document.getElementById('orderAmountS').textContent = (d.money || '--') + (d.fiat ? ' ' + d.fiat : '');
        document.getElementById('orderIdS').textContent = d.order_id || '--';
        bindHelp('helpBtnS', d.support_url);
        Payment.init({
            expired_at: d.expired_at,
            created_at: d.created_at,
            trade_id: d.trade_id,
            return_url: d.redirect_url,
            status: d.status
        });
    }

    function updateReselectButton() {
        var btn = document.getElementById('reselectPaymentBtn');
        if (!btn) return;
        btn.style.display = orderData && orderData.reselect ? 'inline-flex' : 'none';
        if (!btn.dataset.bound) {
            btn.dataset.bound = '1';
            btn.addEventListener('click', function () {
                showSelection();
            });
        }
    }

    function bindManualTxSubmit(data) {
        var section = document.getElementById('manualTxSection');
        var modal = document.getElementById('manualTxModal');
        var form = document.getElementById('manualTxForm');
        var toggle = document.getElementById('manualTxToggle');
        var close = document.getElementById('manualTxClose');
        var cancel = document.getElementById('manualTxCancel');
        var input = document.getElementById('manualTxInput');
        var submit = document.getElementById('manualTxSubmit');
        var message = document.getElementById('manualTxMessage');
        if (!section || !modal || !form || !toggle || !close || !cancel || !input || !submit || !message) return;

        function resetModal() {
            form._manualTxRequestId = (form._manualTxRequestId || 0) + 1;
            input.value = '';
            input.disabled = false;
            submit.disabled = false;
            message.textContent = '';
            message.className = 'manual-tx-message';
        }

        function closeModal(restoreFocus) {
            modal.style.display = 'none';
            toggle.setAttribute('aria-expanded', 'false');
            resetModal();
            if (restoreFocus) toggle.focus();
        }

        function openModal() {
            modal.style.display = 'flex';
            toggle.setAttribute('aria-expanded', 'true');
            setTimeout(function () { input.focus(); }, 0);
        }

        var provider = String((data && data.provider) || '').toLowerCase();
        var available = !!(data && data.manual_tx_submit) &&
            data.payment_kind !== 'exchange' && provider !== 'binance' && provider !== 'okx';
        section.hidden = !available;
        if (!available) {
            closeModal(false);
            return;
        }

        if (!toggle.dataset.bound) {
            toggle.dataset.bound = '1';
            toggle.addEventListener('click', openModal);
        }
        if (!close.dataset.bound) {
            close.dataset.bound = '1';
            close.addEventListener('click', function () { closeModal(true); });
        }
        if (!cancel.dataset.bound) {
            cancel.dataset.bound = '1';
            cancel.addEventListener('click', function () { closeModal(true); });
        }
        if (!modal.dataset.bound) {
            modal.dataset.bound = '1';
            modal.addEventListener('click', function (event) {
                if (event.target === modal) closeModal(true);
            });
            document.addEventListener('keydown', function (event) {
                if (event.key === 'Escape' && modal.style.display === 'flex') closeModal(true);
            });
        }
        if (form.dataset.bound) return;
        form.dataset.bound = '1';
        form.addEventListener('submit', function (event) {
            event.preventDefault();
            var txHash = input.value.trim();
            message.className = 'manual-tx-message';
            if (!txHash) {
                message.textContent = _t('manualTxRequired', '请输入交易哈希');
                message.classList.add('error');
                return;
            }

            var requestId = (form._manualTxRequestId || 0) + 1;
            form._manualTxRequestId = requestId;
            submit.disabled = true;
            message.textContent = _t('manualTxVerifying', '正在验证链上交易...');
            fetch('/api/v1/pay/submit-tx-hash', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    trade_id: (orderData && orderData.trade_id) || gTradeId,
                    tx_hash: txHash
                })
            })
                .then(function (response) { return response.json(); })
                .then(function (res) {
                    if (form._manualTxRequestId !== requestId) return;
                    if (res.status_code !== 200) {
                        throw new Error(res.message || _t('manualTxFailed', '交易验证失败'));
                    }
                    input.disabled = true;
                    message.textContent = _t('manualTxAccepted', '已提交，正在确认链上交易');
                    message.classList.add('success');
                    closeModal(false);
                    var confirming = document.getElementById('confirmingModal');
                    if (confirming) confirming.style.display = 'flex';
                })
                .catch(function (error) {
                    if (form._manualTxRequestId !== requestId) return;
                    submit.disabled = false;
                    message.textContent = error.message || _t('manualTxFailed', '交易验证失败');
                    message.classList.add('error');
                });
        });
    }

    function bindPaymentReview(data) {
        var section = document.getElementById('paymentReviewSection');
        var modal = document.getElementById('paymentReviewModal');
        var form = document.getElementById('paymentReviewForm');
        var toggle = document.getElementById('paymentReviewToggle');
        var close = document.getElementById('paymentReviewClose');
        var cancel = document.getElementById('paymentReviewCancel');
        var description = document.getElementById('paymentReviewDescription');
        var transactionHash = document.getElementById('paymentReviewTransactionHash');
        var evidence = document.getElementById('paymentReviewEvidence');
        var submit = document.getElementById('paymentReviewSubmit');
        var message = document.getElementById('paymentReviewMessage');
        if (!section || !modal || !form || !toggle || !close || !cancel || !description || !transactionHash || !evidence || !submit || !message) return;

        var available = !!data && window.Payment.paymentReviewAvailable(data.status);
        section.hidden = !available;
        if (!available) return;

        function reset() {
            description.value = '';
            transactionHash.value = '';
            evidence.value = '';
            message.textContent = '';
            message.className = 'manual-tx-message';
            submit.disabled = false;
        }
        function closeModal(restoreFocus) {
            modal.style.display = 'none';
            toggle.setAttribute('aria-expanded', 'false');
            reset();
            if (restoreFocus) toggle.focus();
        }
        function openModal() {
            var timeoutModal = document.getElementById('timeoutModal');
            if (timeoutModal) timeoutModal.style.display = 'none';
            modal.style.display = 'flex';
            toggle.setAttribute('aria-expanded', 'true');
            setTimeout(function () { description.focus(); }, 0);
        }
        window.openPaymentReview = openModal;
        if (!toggle.dataset.bound) {
            toggle.dataset.bound = '1';
            toggle.addEventListener('click', openModal);
            close.addEventListener('click', function () { closeModal(true); });
            cancel.addEventListener('click', function () { closeModal(true); });
            modal.addEventListener('click', function (event) {
                if (event.target === modal) closeModal(true);
            });
        }
        if (form.dataset.bound) return;
        form.dataset.bound = '1';
        form.addEventListener('submit', function (event) {
            event.preventDefault();
            var file = evidence.files && evidence.files[0];
            if (!file || !transactionHash.value.trim() || description.value.trim().length < 10) {
                message.textContent = '请填写交易编号、至少10个字的付款说明并上传截图';
                message.className = 'manual-tx-message error';
                return;
            }
            submit.disabled = true;
            message.textContent = '正在提交复核...';
            var body = new FormData();
            body.append('trade_id', (orderData && orderData.trade_id) || gTradeId);
            body.append('description', description.value.trim());
            body.append('transaction_hash', transactionHash.value.trim());
            body.append('evidence', file);
            fetch('/api/v1/pay/payment-review', { method: 'POST', body: body })
                .then(function (response) { return response.json(); })
                .then(function (res) {
                    if (res.status_code !== 201 && res.code !== 201) throw new Error(res.message || res.msg || '复核提交失败');
                    message.textContent = '人工复核申请已提交，请等待人工审核处理';
                    message.className = 'manual-tx-message success';
                    setTimeout(function () { closeModal(false); }, 900);
                })
                .catch(function (error) {
                    submit.disabled = false;
                    message.textContent = error.message || '复核提交失败';
                    message.className = 'manual-tx-message error';
                });
        });
    }


    function showPayment() {
        document.getElementById('selectionStage').style.display = 'none';
        document.getElementById('paymentStage').style.display = 'block';
        var d = orderData;
        var provider = String(d.provider || '').toLowerCase();
        var isExchangePayment = d.payment_kind === 'exchange' || provider === 'binance' || provider === 'okx';
        var currency = (d.network && d.network.crypto) ? d.network.crypto : (d.selected_payment ? d.selected_payment.currency : 'USDT');
        var netName = (d.network && d.network.name) ? d.network.name : (d.selected_payment ? d.selected_payment.token_net_name : '');
        var netKey = (d.network && (d.network.network || d.network.key || d.network.name)) ? (d.network.network || d.network.key || d.network.name) : (d.selected_payment ? d.selected_payment.network : netName);
        var amount = d.actual_amount || '--';
        window._qrAmount = amount;
        document.getElementById('orderAmountQ').textContent = (d.money || '--') + (d.fiat ? ' ' + d.fiat : '');
        document.getElementById('payAmountQ').textContent = amount + ' ' + currency;
        var payNetworkQ = document.getElementById('payNetworkQ');
        if (window.renderNetworkTag) {
            window.renderNetworkTag(payNetworkQ, netName || '--', netKey);
        } else if (payNetworkQ) {
            payNetworkQ.textContent = netName || '--';
        }
        var paymentNoticeQ = document.getElementById('paymentNoticeQ');
        var paymentNoticeTextQ = document.getElementById('paymentNoticeTextQ');
        if (paymentNoticeQ && paymentNoticeTextQ) {
            var noticeNetwork = netName;
            if (provider === 'binance') noticeNetwork = 'Binance币安交易所';
            if (provider === 'okx') noticeNetwork = 'OKX欧易交易所';
            renderPaymentNotice(paymentNoticeTextQ, provider, noticeNetwork);
            paymentNoticeQ.hidden = !netName;
        }
        document.getElementById('orderIdQ').textContent = d.order_id || '--';
        var paymentAddress = d.token || d.address || '';
        if (isExchangePayment) {
            document.getElementById('walletAddress').textContent = paymentAddress || '--';
        } else {
            renderWalletAddress(paymentAddress);
        }
        var addr = document.getElementById('addressLabelQ');
        if (addr) {
            if (provider === 'okx') addr.textContent = '欧易UID';
            else if (provider === 'binance') addr.textContent = '币安ID';
            else addr.textContent = _t('receivingAddress', '收款地址');
        }
        var copyAddressHint = document.getElementById('copyAddressHint');
        if (copyAddressHint) {
            copyAddressHint.hidden = !isExchangePayment;
            copyAddressHint.dataset.defaultLabel = provider === 'binance' ? '复制ID' : '复制UID';
            copyAddressHint.textContent = copyAddressHint.dataset.defaultLabel;
        }
        var copyAddressBtn = document.getElementById('copyAddressBtn');
        if (copyAddressBtn) {
            copyAddressBtn.title = isExchangePayment
                ? (provider === 'binance' ? '复制ID' : '复制UID')
                : _t('copyAddressTitle', '复制地址');
        }
        var paymentQrQ = document.getElementById('paymentQrQ');
        if (paymentQrQ) paymentQrQ.style.display = isExchangePayment ? 'none' : '';
        $('#qrcode').empty();
        if (!isExchangePayment) {
            $('#qrcode').qrcode({ text: paymentAddress, width: 170, height: 170 });
        }
        if (window.Payment && typeof Payment.updateQrPaymentLogo === 'function') {
            Payment.updateQrPaymentLogo(isExchangePayment ? '' : currency, isExchangePayment ? '' : netKey);
        }
        updateReselectButton();
        bindManualTxSubmit(d);
        bindPaymentReview(d);
        bindHelp('helpBtnQ', d.support_url);
        Payment.initQrPage({
            expired_at: d.expired_at,
            created_at: d.created_at,
            trade_id: d.trade_id,
            return_url: d.redirect_url,
            status: d.status,
            manual_tx_submit: d.manual_tx_submit
        });
    }

    function bindHelp(id, url) {
        var el = document.getElementById(id);
        if (!el) return;
        var safeUrl = safeHttpsUrl(url, '');
        if (safeUrl) {
            el.href = safeUrl;
            el.removeAttribute('aria-disabled');
            el.style.pointerEvents = '';
            el.style.opacity = '';
        } else {
            el.href = '#';
            el.setAttribute('aria-disabled', 'true');
            el.style.pointerEvents = 'none';
            el.style.opacity = '0.35';
        }
    }

    function loadOrder() {
        fetch('/api/v1/pay/info', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ trade_id: gTradeId })
        })
            .then(function (r) { return r.json(); })
            .then(function (res) {
                if (res.status_code !== 200) {
                    var t = document.getElementById('cmusToast');
                    if (t) {
                        t.textContent = res.message || _t('toastLoadOrderFailed', '加载订单失败');
                        t.className = 'toast error show';
                    }
                    return;
                }
                orderData = res.data;
                if (orderData.status === 4) {
                    if (window.Payment && Payment.showCanceled) Payment.showCanceled(orderData);
                    return;
                }
                if (!orderData.trade_type || !orderData.token) showSelection();
                else showPayment();
            })
            .catch(function (e) {
                var t = document.getElementById('cmusToast');
                if (t) {
                    t.textContent = _t('toastNetworkError', '网络错误') + ': ' + e.message;
                    t.className = 'toast error show';
                }
            });
    }

    document.addEventListener('DOMContentLoaded', function () {
        var parts = window.location.pathname.split('/');
        gTradeId = parts[parts.length - 1];
        if (!gTradeId) return;
        var ready = (window.Payment && Payment.initI18n) ? Payment.initI18n() : Promise.resolve();
        ready.then(loadOrder);
    });
})();
