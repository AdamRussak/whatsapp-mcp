class WebhookManager {
    constructor() {
        // Determine the API base URL based on current location
        const currentPort = window.location.port;
        const currentProtocol = window.location.protocol;
        const currentHost = window.location.hostname; // Use the browser's hostname, not container name

        // For containerized environments, use the host's port mapping
        // The backend is accessible through the host on port 8080
        this.apiBaseUrl = `${currentProtocol}//${currentHost}:8080/api`;
        
        console.log('API Base URL:', this.apiBaseUrl);
        this.webhooks = [];
        this.currentEditingId = null;
        this.currentLogsWebhookId = null;
        this.chatPickerSeq = 0;
        this.downloads = { offset: 0, limit: 25, total: 0, loaded: false };
        this.init();
    }

    async checkApiConnection() {
        try {
            console.log('Checking API connection to:', `${this.apiBaseUrl}/webhooks`);
            const response = await fetch(`${this.apiBaseUrl}/webhooks`);
            console.log('API response status:', response.status);
            console.log('API response ok:', response.ok);
            return response.ok || response.status === 404; // 404 is ok, means API is running but no webhooks
        } catch (error) {
            console.error('API connection failed:', error);
            return false;
        }
    }

    async init() {
        this.setupEventListeners();
        
        // Check API connection first
        const isConnected = await this.checkApiConnection();
        if (!isConnected) {
            this.showToast('Cannot connect to WhatsApp Bridge API. Make sure it\'s running on port 8080.', 'error');
        }
        
        this.loadWebhooks();
    }

    setupEventListeners() {
        // Modal controls
        document.getElementById('addWebhookBtn').addEventListener('click', () => this.openModal());
        document.getElementById('closeModal').addEventListener('click', () => this.closeModal());
        document.getElementById('cancelBtn').addEventListener('click', () => this.closeModal());
        document.getElementById('refreshBtn').addEventListener('click', () => this.loadWebhooks());

        // Logs modal controls
        document.getElementById('closeLogsModal').addEventListener('click', () => this.closeLogsModal());
        document.getElementById('refreshLogsBtn').addEventListener('click', () => this.refreshCurrentLogs());

        // Form submission
        document.getElementById('webhookForm').addEventListener('submit', (e) => this.handleFormSubmit(e));

        // Auto-download reveals the voice-only choice and waives the URL requirement
        document.getElementById('autoDownload').addEventListener('change', () => this.syncAutoDownloadFields());

        // Add trigger button
        document.getElementById('addTriggerBtn').addEventListener('click', () => this.addTriggerRow());

        // Toast close
        document.getElementById('toastClose').addEventListener('click', () => this.hideToast());

        // Views: listeners, and the media they captured
        document.querySelectorAll('.view-tab').forEach(tab => {
            tab.addEventListener('click', () => this.switchView(tab.dataset.view));
        });

        // Downloads filters and paging
        document.getElementById('downloadsFilters').addEventListener('submit', (e) => {
            e.preventDefault();
            this.downloads.offset = 0;
            this.loadDownloads();
        });
        document.getElementById('downloadsReset').addEventListener('click', () => {
            document.getElementById('downloadsFilters').reset();
            this.downloads.offset = 0;
            this.loadDownloads();
        });
        document.getElementById('downloadsPrev').addEventListener('click', () => {
            this.downloads.offset = Math.max(0, this.downloads.offset - this.downloads.limit);
            this.loadDownloads();
        });
        document.getElementById('downloadsNext').addEventListener('click', () => {
            this.downloads.offset += this.downloads.limit;
            this.loadDownloads();
        });

        // Close modals when clicking outside
        window.addEventListener('click', (e) => {
            const webhookModal = document.getElementById('webhookModal');
            const logsModal = document.getElementById('logsModal');
            if (e.target === webhookModal) {
                this.closeModal();
            }
            if (e.target === logsModal) {
                this.closeLogsModal();
            }
        });

        // Setup initial trigger row event listener
        this.setupTriggerRowListeners(document.querySelector('.trigger-item'));
    }

    async loadWebhooks() {
        try {
            console.log('Loading webhooks from:', `${this.apiBaseUrl}/webhooks`);
            this.showLoading(true);
            const response = await fetch(`${this.apiBaseUrl}/webhooks`);
            
            console.log('Webhooks response status:', response.status);
            console.log('Webhooks response ok:', response.ok);
            
            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }
            
            const data = await response.json();
            console.log('Webhook response:', data); // Debug log
            
            // Handle different response formats
            if (data.success && Array.isArray(data.data)) {
                this.webhooks = data.data;
            } else if (Array.isArray(data)) {
                this.webhooks = data;
            } else {
                this.webhooks = [];
            }
            
            console.log('Processed webhooks:', this.webhooks);
            this.renderWebhooks();
        } catch (error) {
            console.error('Failed to load webhooks:', error);
            this.showToast('Failed to load webhooks: ' + error.message, 'error');
            this.webhooks = [];
            this.renderWebhooks(); // Show empty state
        } finally {
            this.showLoading(false);
        }
    }

    renderWebhooks() {
        const container = document.getElementById('webhooksList');
        const noDataElement = document.getElementById('noWebhooks');
        
        container.innerHTML = '';
        
        console.log('Rendering webhooks:', this.webhooks); // Debug log
        
        if (!this.webhooks || this.webhooks.length === 0) {
            noDataElement.style.display = 'block';
            container.style.display = 'none';
            return;
        }
        
        noDataElement.style.display = 'none';
        container.style.display = 'grid';
        
        this.webhooks.forEach(webhook => {
            const webhookElement = this.createWebhookCard(webhook);
            container.appendChild(webhookElement);
        });
    }

    createWebhookCard(webhook) {
        const template = document.getElementById('webhookCardTemplate');
        const clone = template.content.cloneNode(true);
        
        // Fill in the data
        clone.querySelector('.webhook-name').textContent = webhook.name;
        clone.querySelector('.webhook-url').textContent = webhook.webhook_url || '— (download-only listener)';

        // Auto-download badge
        const autoDownloadBadge = clone.querySelector('.webhook-autodownload');
        if (webhook.auto_download) {
            autoDownloadBadge.style.display = '';
            autoDownloadBadge.title = webhook.auto_download_voice_only === false
                ? 'Downloads all audio from matching messages'
                : 'Downloads voice notes from matching messages';
        }
        clone.querySelector('.webhook-created').textContent = this.formatDate(webhook.created_at);
        
        // Status
        const statusElement = clone.querySelector('.webhook-status');
        statusElement.textContent = webhook.enabled ? 'Enabled' : 'Disabled';
        statusElement.className = `webhook-status ${webhook.enabled ? 'enabled' : 'disabled'}`;
        
        // Triggers
        const triggersText = this.formatTriggers(webhook.triggers);
        clone.querySelector('.webhook-triggers').textContent = triggersText;
        
        // Setup action buttons
        const card = clone.querySelector('.webhook-card');
        card.dataset.webhookId = webhook.id;
        
        clone.querySelector('.btn-test').addEventListener('click', () => this.testWebhook(webhook.id));
        clone.querySelector('.btn-logs').addEventListener('click', () => this.viewLogs(webhook.id));
        clone.querySelector('.btn-edit').addEventListener('click', () => this.editWebhook(webhook));
        clone.querySelector('.btn-toggle').addEventListener('click', () => this.toggleWebhook(webhook));
        clone.querySelector('.btn-delete').addEventListener('click', () => this.deleteWebhook(webhook.id, webhook.name));
        
        return clone;
    }

    formatDate(dateString) {
        if (!dateString) return 'Unknown';
        try {
            return new Date(dateString).toLocaleDateString() + ' ' + new Date(dateString).toLocaleTimeString();
        } catch (error) {
            return 'Invalid date';
        }
    }

    formatTriggers(triggers) {
        if (!triggers || triggers.length === 0) {
            return 'No triggers configured';
        }
        
        return triggers.map(trigger => {
            if (trigger.trigger_type === 'all') {
                return 'All messages';
            }
            return `${trigger.trigger_type}: ${trigger.trigger_value}`;
        }).join(', ');
    }

    openModal(webhook = null) {
        const modal = document.getElementById('webhookModal');
        const title = document.getElementById('modalTitle');
        const submitText = document.getElementById('submitText');
        
        this.currentEditingId = webhook ? webhook.id : null;
        
        if (webhook) {
            title.textContent = 'Edit Webhook';
            submitText.textContent = 'Update Webhook';
            this.populateForm(webhook);
        } else {
            title.textContent = 'Add Webhook';
            submitText.textContent = 'Create Webhook';
            this.resetForm();
        }
        
        modal.style.display = 'block';
    }

    closeModal() {
        const modal = document.getElementById('webhookModal');
        modal.style.display = 'none';
        this.currentEditingId = null;
    }

    resetForm() {
        document.getElementById('webhookForm').reset();
        document.getElementById('webhookEnabled').checked = true;
        document.getElementById('autoDownload').checked = false;
        document.getElementById('autoDownloadVoiceOnly').checked = true;
        this.syncAutoDownloadFields();
        
        // Reset triggers to one default row
        const container = document.getElementById('triggersContainer');
        container.innerHTML = `
            <div class="trigger-item">
                <select name="trigger_type" class="trigger-type">
                    <option value="all">All Messages</option>
                    <option value="chat_jid">Specific Chat</option>
                    <option value="sender">Specific Sender</option>
                    <option value="keyword">Keyword</option>
                    <option value="media_type">Media Type</option>
                </select>
                <input type="text" name="trigger_value" class="trigger-value" placeholder="Trigger value">
                <select name="match_type" class="match-type">
                    <option value="exact">Exact</option>
                    <option value="contains">Contains</option>
                    <option value="regex">Regex</option>
                </select>
                <button type="button" class="btn-remove-trigger" title="Remove trigger">
                    <i class="fas fa-trash"></i>
                </button>
            </div>
        `;
        this.setupTriggerRowListeners(container.querySelector('.trigger-item'));
    }

    // syncAutoDownloadFields keeps the form honest about what auto-download changes: the
    // voice-only choice only exists when it is on, and the URL stops being mandatory.
    syncAutoDownloadFields() {
        const on = document.getElementById('autoDownload').checked;
        document.getElementById('voiceOnlyGroup').style.display = on ? '' : 'none';
        document.getElementById('webhookURLLabel').textContent = on ? 'Webhook URL' : 'Webhook URL *';
        document.getElementById('webhookURLHint').style.display = on ? '' : 'none';
    }

    populateForm(webhook) {
        document.getElementById('webhookName').value = webhook.name;
        document.getElementById('webhookURL').value = webhook.webhook_url || '';
        document.getElementById('secretToken').value = webhook.secret_token || '';
        document.getElementById('webhookEnabled').checked = webhook.enabled;
        document.getElementById('autoDownload').checked = !!webhook.auto_download;
        document.getElementById('autoDownloadVoiceOnly').checked = webhook.auto_download_voice_only !== false;
        this.syncAutoDownloadFields();
        
        // Populate triggers
        const container = document.getElementById('triggersContainer');
        container.innerHTML = '';
        
        if (webhook.triggers && webhook.triggers.length > 0) {
            webhook.triggers.forEach(trigger => {
                this.addTriggerRow(trigger);
            });
        } else {
            this.addTriggerRow();
        }
    }

    addTriggerRow(triggerData = null) {
        const container = document.getElementById('triggersContainer');
        const triggerHtml = `
            <div class="trigger-item">
                <select name="trigger_type" class="trigger-type">
                    <option value="all">All Messages</option>
                    <option value="chat_jid">Specific Chat</option>
                    <option value="sender">Specific Sender</option>
                    <option value="keyword">Keyword</option>
                    <option value="media_type">Media Type</option>
                </select>
                <input type="text" name="trigger_value" class="trigger-value" placeholder="Trigger value">
                <select name="match_type" class="match-type">
                    <option value="exact">Exact</option>
                    <option value="contains">Contains</option>
                    <option value="regex">Regex</option>
                </select>
                <button type="button" class="btn-remove-trigger" title="Remove trigger">
                    <i class="fas fa-trash"></i>
                </button>
            </div>
        `;
        
        container.insertAdjacentHTML('beforeend', triggerHtml);
        const newRow = container.lastElementChild;
        
        if (triggerData) {
            newRow.querySelector('.trigger-type').value = triggerData.trigger_type;
            newRow.querySelector('.trigger-value').value = triggerData.trigger_value;
            newRow.querySelector('.match-type').value = triggerData.match_type;
        }
        
        this.setupTriggerRowListeners(newRow);
    }

    setupTriggerRowListeners(triggerRow) {
        const removeBtn = triggerRow.querySelector('.btn-remove-trigger');
        const typeSelect = triggerRow.querySelector('.trigger-type');
        const valueInput = triggerRow.querySelector('.trigger-value');
        
        removeBtn.addEventListener('click', () => {
            const container = document.getElementById('triggersContainer');
            if (container.children.length > 1) {
                triggerRow.remove();
            }
        });
        
        typeSelect.addEventListener('change', () => {
            if (typeSelect.value === 'all') {
                valueInput.disabled = true;
                valueInput.value = '';
                valueInput.placeholder = 'No value needed for "all messages"';
                valueInput.removeAttribute('list');
            } else if (typeSelect.value === 'media_type') {
                valueInput.disabled = false;
                valueInput.placeholder = 'Select or type media type (e.g., poll, image)';
                valueInput.setAttribute('list', 'mediaTypeOptions');
            } else if (typeSelect.value === 'chat_jid' || typeSelect.value === 'sender') {
                valueInput.disabled = false;
                // A sender trigger matches a person, so only 1:1 chats are offered; a group
                // participant still has to be typed as a number.
                const kind = typeSelect.value === 'sender' ? 'contact' : 'all';
                valueInput.placeholder = kind === 'contact'
                    ? 'Search a contact, or type a phone number'
                    : 'Search a chat or group, or type a JID';
                this.attachChatPicker(triggerRow, valueInput, kind);
            } else {
                valueInput.disabled = false;
                valueInput.placeholder = 'Trigger value';
                valueInput.removeAttribute('list');
            }
        });
        
        // Trigger the change event to set initial state
        typeSelect.dispatchEvent(new Event('change'));
    }

    // attachChatPicker backs one trigger row's value field with a live list of chats.
    // Free text still submits, which matters because a phone number is a valid value: the
    // bridge expands it to the @lid chat the contact actually writes from.
    attachChatPicker(triggerRow, valueInput, kind) {
        let datalist = triggerRow.querySelector('datalist.chat-options');
        if (!datalist) {
            datalist = document.createElement('datalist');
            datalist.className = 'chat-options';
            datalist.id = `chatOptions${++this.chatPickerSeq}`;
            triggerRow.appendChild(datalist);
        }
        valueInput.setAttribute('list', datalist.id);

        if (valueInput.dataset.chatPickerKind === kind) {
            return; // already wired for this kind; don't stack listeners
        }
        valueInput.dataset.chatPickerKind = kind;

        const refresh = async () => {
            const chats = await this.fetchChats(valueInput.value.trim(), valueInput.dataset.chatPickerKind);
            this.renderChatOptions(datalist, chats);
        };

        if (!valueInput.dataset.chatPickerBound) {
            valueInput.dataset.chatPickerBound = 'true';
            let debounce = null;
            valueInput.addEventListener('input', () => {
                clearTimeout(debounce);
                debounce = setTimeout(refresh, 250);
            });
        }

        refresh();
    }

    async fetchChats(query, kind) {
        const params = new URLSearchParams({ limit: '25' });
        if (query) params.set('q', query);
        if (kind && kind !== 'all') params.set('type', kind);

        try {
            const response = await fetch(`${this.apiBaseUrl}/chats?${params}`);
            if (!response.ok) return [];
            const data = await response.json();
            return Array.isArray(data.data) ? data.data : [];
        } catch (error) {
            // A picker that cannot reach the bridge must not block typing a value by hand.
            console.error('Failed to load chats:', error);
            return [];
        }
    }

    renderChatOptions(datalist, chats) {
        datalist.innerHTML = '';
        chats.forEach(chat => {
            const option = document.createElement('option');
            // The value is what lands in the trigger; the label is what the user reads.
            option.value = chat.jid;
            const name = chat.name && chat.name !== chat.jid.split('@')[0] ? chat.name : '';
            option.textContent = name
                ? `${name}${chat.is_group ? ' (group)' : ''}`
                : (chat.is_group ? 'Group' : 'Contact');
            datalist.appendChild(option);
        });
    }

    // switchView shows one of the two top-level views. Downloads load on first visit only;
    // after that the Apply button refreshes them, so switching back and forth is instant.
    switchView(view) {
        const showDownloads = view === 'downloads';
        document.getElementById('webhooksView').hidden = showDownloads;
        document.getElementById('downloadsView').hidden = !showDownloads;
        document.querySelectorAll('.view-tab').forEach(tab => {
            const active = tab.dataset.view === view;
            tab.classList.toggle('active', active);
            tab.setAttribute('aria-selected', active ? 'true' : 'false');
        });

        if (showDownloads && !this.downloads.loaded) {
            this.downloads.loaded = true;
            const chatInput = document.getElementById('downloadsChat');
            this.attachChatPicker(chatInput.closest('.filter-field'), chatInput, 'all');
            this.loadDownloads();
        }
    }

    // downloadsQuery turns the filter form into API parameters. Dates are local days: the
    // bridge compares instants, so "From 20 Sep" becomes local midnight on the 20th and "To
    // 20 Sep" becomes local midnight on the 21st, making the To day inclusive. Sending bare
    // dates would mean UTC midnight and shift a day's edges by the viewer's offset.
    downloadsQuery() {
        const params = new URLSearchParams({
            limit: String(this.downloads.limit),
            offset: String(this.downloads.offset),
            status: document.getElementById('downloadsStatus').value,
        });

        const chat = document.getElementById('downloadsChat').value.trim();
        if (chat) params.set('chat', chat);

        const from = document.getElementById('downloadsFrom').value;
        if (from) params.set('after', new Date(`${from}T00:00:00`).toISOString());

        const to = document.getElementById('downloadsTo').value;
        if (to) {
            const end = new Date(`${to}T00:00:00`);
            end.setDate(end.getDate() + 1);
            params.set('before', end.toISOString());
        }
        return params;
    }

    async loadDownloads() {
        const loading = document.getElementById('downloadsLoading');
        loading.hidden = false;
        try {
            const response = await fetch(`${this.apiBaseUrl}/downloads?${this.downloadsQuery()}`);
            const data = await response.json();
            if (!response.ok) {
                throw new Error(data.message || data.error || `HTTP ${response.status}`);
            }
            this.downloads.total = data.total || 0;
            this.renderDownloads(Array.isArray(data.data) ? data.data : []);
        } catch (error) {
            console.error('Failed to load downloads:', error);
            this.showToast('Failed to load downloads: ' + error.message, 'error');
            this.renderDownloads([]);
        } finally {
            loading.hidden = true;
        }
    }

    renderDownloads(items) {
        const body = document.getElementById('downloadsBody');
        body.innerHTML = '';
        items.forEach(item => body.appendChild(this.createDownloadRow(item)));

        const hasItems = items.length > 0;
        document.getElementById('downloadsTable').hidden = !hasItems;
        document.getElementById('noDownloads').hidden = hasItems;

        const { offset, limit, total } = this.downloads;
        const pager = document.getElementById('downloadsPager');
        pager.hidden = total <= limit;
        document.getElementById('downloadsRange').textContent =
            hasItems ? `${offset + 1}–${offset + items.length} of ${total}` : `0 of ${total}`;
        document.getElementById('downloadsPrev').disabled = offset === 0;
        document.getElementById('downloadsNext').disabled = offset + items.length >= total;
    }

    // createDownloadRow builds one row. Chat names, senders and errors come from other
    // people's WhatsApp accounts, so every value is set with textContent — never innerHTML.
    createDownloadRow(item) {
        const row = document.createElement('tr');
        row.dataset.messageId = item.message_id;
        if (item.status !== 'ok' || item.file_missing) row.classList.add('download-unavailable');

        const cell = (text, className) => {
            const td = document.createElement('td');
            if (className) td.className = className;
            td.textContent = text;
            row.appendChild(td);
            return td;
        };

        // The UI builds media URLs from its own API base, not the bridge's download_url:
        // that is PUBLIC_BASE_URL, the container's DNS name, which a browser cannot resolve.
        const mediaUrl = item.media_path
            ? `${this.apiBaseUrl}/media/${item.media_path.split('/').map(encodeURIComponent).join('/')}`
            : '';

        const play = cell('', 'col-play');
        if (mediaUrl) {
            const audio = document.createElement('audio');
            audio.controls = true;
            audio.preload = 'none';
            audio.src = mediaUrl;
            play.appendChild(audio);
        } else {
            play.textContent = item.file_missing ? 'File missing' : this.downloadStatusLabel(item.status);
            if (item.error) play.title = item.error;
        }

        const chat = cell(item.chat_name || item.chat_jid);
        chat.title = item.chat_jid;

        cell(item.timestamp ? new Date(item.timestamp).toLocaleString() : '—');
        cell(item.is_from_me ? 'Me' : (item.sender || '—'));
        cell(this.formatDuration(item.seconds), 'col-num');
        cell(item.size_bytes ? this.formatBytes(item.size_bytes) : '—', 'col-num');

        const save = cell('', 'col-save');
        if (mediaUrl) {
            const link = document.createElement('a');
            link.href = `${mediaUrl}?download=1`;
            link.className = 'btn-action';
            link.title = 'Save this file';
            link.setAttribute('aria-label', 'Save this file');
            const icon = document.createElement('i');
            icon.className = 'fas fa-download';
            link.appendChild(icon);
            save.appendChild(link);
        }

        return row;
    }

    downloadStatusLabel(status) {
        const labels = {
            ok: 'Downloaded',
            failed: 'Failed',
            retry_timeout: 'Phone did not answer',
            expired_not_on_phone: 'Expired',
            rekeyed_on_phone: 'Unrecoverable',
            incomplete_metadata: 'No key stored',
        };
        return labels[status] || status;
    }

    formatDuration(seconds) {
        if (!seconds) return '—';
        const m = Math.floor(seconds / 60);
        const s = String(seconds % 60).padStart(2, '0');
        return `${m}:${s}`;
    }

    formatBytes(bytes) {
        if (bytes < 1024) return `${bytes} B`;
        if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
        return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    }

    async handleFormSubmit(e) {
        e.preventDefault();
        
        const formData = new FormData(e.target);
        const webhook = {
            name: formData.get('name')?.trim(),
            webhook_url: formData.get('webhook_url')?.trim(),
            secret_token: formData.get('secret_token')?.trim() || '',
            enabled: document.getElementById('webhookEnabled').checked,
            auto_download: document.getElementById('autoDownload').checked,
            auto_download_voice_only: document.getElementById('autoDownloadVoiceOnly').checked,
            triggers: this.collectTriggers()
        };
        
        // Validation
        if (!webhook.name) {
            this.showToast('Webhook name is required', 'error');
            return;
        }
        
        // A listener that only auto-downloads posts nowhere, so it needs no URL.
        if (!webhook.webhook_url && !webhook.auto_download) {
            this.showToast('Webhook URL is required unless auto-download is enabled', 'error');
            return;
        }
        
        if (webhook.webhook_url) {
            try {
                new URL(webhook.webhook_url);
            } catch (error) {
                this.showToast('Please enter a valid webhook URL', 'error');
                return;
            }
        }
        
        if (webhook.triggers.length === 0) {
            this.showToast('At least one trigger is required', 'error');
            return;
        }
        
        console.log('Submitting webhook:', webhook); // Debug log
        
        if (this.currentEditingId) {
            webhook.id = this.currentEditingId;
            await this.updateWebhook(webhook);
        } else {
            await this.createWebhook(webhook);
        }
    }

    collectTriggers() {
        const triggers = [];
        const triggerRows = document.querySelectorAll('.trigger-item');
        
        triggerRows.forEach(row => {
            const type = row.querySelector('.trigger-type').value;
            const value = row.querySelector('.trigger-value').value?.trim() || '';
            const matchType = row.querySelector('.match-type').value;
            
            // For "all" type, we don't need a value
            // For other types, we need a value
            if (type === 'all' || (type !== 'all' && value)) {
                triggers.push({
                    trigger_type: type,
                    trigger_value: type === 'all' ? '' : value,
                    match_type: matchType,
                    enabled: true
                });
            }
        });
        
        console.log('Collected triggers:', triggers); // Debug log
        return triggers;
    }

    async createWebhook(webhook) {
        try {
            console.log('Creating webhook:', webhook); // Debug log
            
            const response = await fetch(`${this.apiBaseUrl}/webhooks`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json'
                },
                body: JSON.stringify(webhook)
            });
            
            console.log('Create response status:', response.status); // Debug log
            
            if (!response.ok) {
                let errorMessage = `HTTP error! status: ${response.status}`;
                try {
                    const errorData = await response.json();
                    errorMessage = errorData.error || errorData.message || errorMessage;
                } catch (e) {
                    // If response is not JSON, use status text
                    errorMessage = response.statusText || errorMessage;
                }
                throw new Error(errorMessage);
            }
            
            const result = await response.json();
            console.log('Create result:', result); // Debug log
            
            this.showToast('Webhook created successfully', 'success');
            this.closeModal();
            this.loadWebhooks();
        } catch (error) {
            console.error('Failed to create webhook:', error);
            this.showToast('Failed to create webhook: ' + error.message, 'error');
        }
    }

    async updateWebhook(webhook) {
        try {
            const response = await fetch(`${this.apiBaseUrl}/webhooks/${webhook.id}`, {
                method: 'PUT',
                headers: {
                    'Content-Type': 'application/json'
                },
                body: JSON.stringify(webhook)
            });
            
            if (!response.ok) {
                const error = await response.json();
                throw new Error(error.error || `HTTP error! status: ${response.status}`);
            }
            
            this.showToast('Webhook updated successfully', 'success');
            this.closeModal();
            this.loadWebhooks();
        } catch (error) {
            console.error('Failed to update webhook:', error);
            this.showToast('Failed to update webhook: ' + error.message, 'error');
        }
    }

    async deleteWebhook(id, name) {
        if (!confirm(`Are you sure you want to delete webhook "${name}"?\n\nThis will also delete all webhook logs and cannot be undone.`)) {
            return;
        }
        
        try {
            const response = await fetch(`${this.apiBaseUrl}/webhooks/${id}`, {
                method: 'DELETE'
            });
            
            if (!response.ok) {
                let errorMessage = `HTTP error! status: ${response.status}`;
                try {
                    const errorData = await response.json();
                    errorMessage = errorData.error || errorData.message || errorMessage;
                } catch (e) {
                    // If response is not JSON, use status text
                    errorMessage = response.statusText || errorMessage;
                }
                throw new Error(errorMessage);
            }
            
            this.showToast('Webhook deleted successfully', 'success');
            this.loadWebhooks();
        } catch (error) {
            console.error('Failed to delete webhook:', error);
            this.showToast('Failed to delete webhook: ' + error.message, 'error');
        }
    }

    async toggleWebhook(webhook) {
        try {
            const response = await fetch(`${this.apiBaseUrl}/webhooks/${webhook.id}/enable`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json'
                },
                body: JSON.stringify({ enabled: !webhook.enabled })
            });
            
            if (!response.ok) {
                const error = await response.json();
                throw new Error(error.error || `HTTP error! status: ${response.status}`);
            }
            
            const action = webhook.enabled ? 'disabled' : 'enabled';
            this.showToast(`Webhook ${action} successfully`, 'success');
            this.loadWebhooks();
        } catch (error) {
            console.error('Failed to toggle webhook:', error);
            this.showToast('Failed to toggle webhook: ' + error.message, 'error');
        }
    }

    async testWebhook(id) {
        try {
            const response = await fetch(`${this.apiBaseUrl}/webhooks/${id}/test`, {
                method: 'POST'
            });
            
            if (!response.ok) {
                const error = await response.json();
                throw new Error(error.error || `HTTP error! status: ${response.status}`);
            }
            
            this.showToast('Webhook test successful', 'success');
        } catch (error) {
            console.error('Failed to test webhook:', error);
            this.showToast('Webhook test failed: ' + error.message, 'error');
        }
    }

    async viewLogs(id) {
        this.currentLogsWebhookId = id;
        this.openLogsModal(id);
        await this.loadWebhookLogs(id);
    }

    openLogsModal(webhookId) {
        const modal = document.getElementById('logsModal');
        const title = document.getElementById('logsModalTitle');
        
        // Find webhook name
        const webhook = this.webhooks.find(w => w.id === webhookId);
        const webhookName = webhook ? webhook.name : `Webhook ${webhookId}`;
        
        title.textContent = `Logs - ${webhookName}`;
        modal.style.display = 'block';
    }

    closeLogsModal() {
        const modal = document.getElementById('logsModal');
        modal.style.display = 'none';
        this.currentLogsWebhookId = null;
    }

    async refreshCurrentLogs() {
        if (this.currentLogsWebhookId) {
            await this.loadWebhookLogs(this.currentLogsWebhookId);
        }
    }

    async loadWebhookLogs(id) {
        try {
            this.showLogsLoading(true);
            
            const response = await fetch(`${this.apiBaseUrl}/webhooks/${id}/logs`);
            
            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }
            
            const data = await response.json();
            const logs = data.data || [];
            
            console.log('Webhook logs:', logs); // Keep console log for debugging
            
            this.renderLogs(logs);
            this.updateLogsCount(logs.length);
            
        } catch (error) {
            console.error('Failed to get webhook logs:', error);
            this.showToast('Failed to get webhook logs: ' + error.message, 'error');
            this.renderLogs([]);
            this.updateLogsCount(0);
        } finally {
            this.showLogsLoading(false);
        }
    }

    renderLogs(logs) {
        const container = document.getElementById('logsList');
        const noLogsElement = document.getElementById('noLogs');
        
        container.innerHTML = '';
        
        if (!logs || logs.length === 0) {
            noLogsElement.style.display = 'block';
            container.style.display = 'none';
            return;
        }
        
        noLogsElement.style.display = 'none';
        container.style.display = 'flex';
        
        // Sort logs by timestamp (newest first)
        const sortedLogs = logs.sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
        
        sortedLogs.forEach(log => {
            const logElement = this.createLogEntry(log);
            container.appendChild(logElement);
        });
    }

    createLogEntry(log) {
        const template = document.getElementById('logEntryTemplate');
        const clone = template.content.cloneNode(true);
        
        // An auto-download capture is an action of the same listener, not an HTTP delivery:
        // it has no status code, and delivered_at carries whether the file landed.
        const isCapture = log.event_type === 'auto_download';

        let status = 'pending';
        if (isCapture) {
            status = log.delivered_at ? 'success' : 'error';
        } else if (log.response_status) {
            status = log.response_status >= 200 && log.response_status < 300 ? 'success' : 'error';
        } else if (log.delivered_at) {
            status = 'success';
        }
        
        // Apply status class to entry
        const logEntry = clone.querySelector('.log-entry');
        logEntry.classList.add(status);
        
        // Fill in the data
        const statusBadge = clone.querySelector('.status-badge');
        if (isCapture) {
            statusBadge.textContent = status === 'success' ? 'CAPTURED' : 'CAPTURE FAILED';
        } else {
            statusBadge.textContent = status.toUpperCase();
        }
        statusBadge.classList.add(status);
        
        clone.querySelector('.log-timestamp').textContent = this.formatDate(log.created_at);
        clone.querySelector('.attempt-number').textContent = log.attempt_count || 1;
        
        // Trigger information
        const triggerInfo = `${log.trigger_type}: ${log.trigger_value}`;
        clone.querySelector('.log-url').textContent = triggerInfo;
        
        // Message ID
        clone.querySelector('.log-message-id').textContent = log.message_id || 'N/A';
        
        // Chat information from payload
        let chatInfo = log.chat_jid || 'N/A';
        try {
            if (log.payload) {
                const payload = JSON.parse(log.payload);
                if (payload.message?.chat_name) {
                    chatInfo = `${payload.message.chat_name} (${log.chat_jid})`;
                }
            }
        } catch (e) {
            // If parsing fails, use chat_jid
        }
        clone.querySelector('.log-chat-info').textContent = chatInfo;
        
        // Response information
        let responseText = 'N/A';
        if (isCapture) {
            responseText = log.response_body || 'N/A';
            let durationMs = null;
            try {
                durationMs = JSON.parse(log.payload || '{}').duration_ms;
            } catch (e) {
                durationMs = null;
            }
            if (durationMs !== null && durationMs !== undefined) {
                responseText += ` (${durationMs}ms)`;
            }
        } else if (log.response_status) {
            responseText = `HTTP ${log.response_status}`;
            if (log.response_body) {
                try {
                    const responseObj = JSON.parse(log.response_body);
                    if (responseObj.message) {
                        responseText += ` - ${responseObj.message.substring(0, 100)}${responseObj.message.length > 100 ? '...' : ''}`;
                    } else {
                        responseText += ` - ${log.response_body.substring(0, 100)}${log.response_body.length > 100 ? '...' : ''}`;
                    }
                } catch (e) {
                    responseText += ` - ${log.response_body.substring(0, 100)}${log.response_body.length > 100 ? '...' : ''}`;
                }
            }
        } else if (log.delivered_at) {
            responseText = 'Delivered successfully';
        }
        clone.querySelector('.log-response').textContent = responseText;
        
        // Processing time from payload metadata
        let processingTime = 0;
        try {
            if (log.payload) {
                const payload = JSON.parse(log.payload);
                processingTime = payload.metadata?.processing_time_ms || 0;
            }
        } catch (e) {
            // If parsing fails, use 0
        }
        clone.querySelector('.log-processing-time').textContent = `${processingTime}ms`;
        
        // Error message (if status indicates error and we have response body)
        if (status === 'error' && log.response_body) {
            const errorElement = clone.querySelector('.log-error');
            errorElement.style.display = 'block';
            
            try {
                const responseObj = JSON.parse(log.response_body);
                const errorMessage = responseObj.message || responseObj.error || log.response_body;
                clone.querySelector('.log-error-message').textContent = errorMessage;
            } catch (e) {
                clone.querySelector('.log-error-message').textContent = log.response_body;
            }
        }
        
        return clone;
    }

    updateLogsCount(count) {
        const logsCount = document.getElementById('logsCount');
        logsCount.textContent = `${count} log ${count === 1 ? 'entry' : 'entries'}`;
    }

    showLogsLoading(show) {
        const loading = document.getElementById('logsLoading');
        const list = document.getElementById('logsList');
        const noData = document.getElementById('noLogs');
        
        if (show) {
            loading.style.display = 'block';
            list.style.display = 'none';
            noData.style.display = 'none';
        } else {
            loading.style.display = 'none';
        }
    }

    editWebhook(webhook) {
        this.openModal(webhook);
    }

    showLoading(show) {
        const spinner = document.getElementById('loadingSpinner');
        const list = document.getElementById('webhooksList');
        const noData = document.getElementById('noWebhooks');
        
        if (show) {
            spinner.style.display = 'block';
            list.style.display = 'none';
            noData.style.display = 'none';
        } else {
            spinner.style.display = 'none';
            list.style.display = 'grid';
        }
    }

    showToast(message, type = 'info') {
        const toast = document.getElementById('toast');
        const messageElement = document.getElementById('toastMessage');
        
        messageElement.textContent = message;
        toast.className = `toast ${type}`;
        toast.style.display = 'flex';
        
        // Auto-hide after 5 seconds
        setTimeout(() => this.hideToast(), 5000);
    }

    hideToast() {
        const toast = document.getElementById('toast');
        toast.style.display = 'none';
    }
}

// Initialize the webhook manager when the page loads
document.addEventListener('DOMContentLoaded', () => {
    new WebhookManager();
});
