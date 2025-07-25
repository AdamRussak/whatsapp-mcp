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

        // Add trigger button
        document.getElementById('addTriggerBtn').addEventListener('click', () => this.addTriggerRow());

        // Toast close
        document.getElementById('toastClose').addEventListener('click', () => this.hideToast());

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
        clone.querySelector('.webhook-url').textContent = webhook.webhook_url;
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

    populateForm(webhook) {
        document.getElementById('webhookName').value = webhook.name;
        document.getElementById('webhookURL').value = webhook.webhook_url;
        document.getElementById('secretToken').value = webhook.secret_token || '';
        document.getElementById('webhookEnabled').checked = webhook.enabled;
        
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
            } else {
                valueInput.disabled = false;
                valueInput.placeholder = 'Trigger value';
            }
        });
        
        // Trigger the change event to set initial state
        typeSelect.dispatchEvent(new Event('change'));
    }

    async handleFormSubmit(e) {
        e.preventDefault();
        
        const formData = new FormData(e.target);
        const webhook = {
            name: formData.get('name')?.trim(),
            webhook_url: formData.get('webhook_url')?.trim(),
            secret_token: formData.get('secret_token')?.trim() || '',
            enabled: document.getElementById('webhookEnabled').checked,
            triggers: this.collectTriggers()
        };
        
        // Validation
        if (!webhook.name) {
            this.showToast('Webhook name is required', 'error');
            return;
        }
        
        if (!webhook.webhook_url) {
            this.showToast('Webhook URL is required', 'error');
            return;
        }
        
        try {
            new URL(webhook.webhook_url);
        } catch (error) {
            this.showToast('Please enter a valid webhook URL', 'error');
            return;
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
        
        // Determine status based on response_status or delivered_at
        let status = 'pending';
        if (log.response_status) {
            status = log.response_status >= 200 && log.response_status < 300 ? 'success' : 'error';
        } else if (log.delivered_at) {
            status = 'success';
        }
        
        // Apply status class to entry
        const logEntry = clone.querySelector('.log-entry');
        logEntry.classList.add(status);
        
        // Fill in the data
        const statusBadge = clone.querySelector('.status-badge');
        statusBadge.textContent = status.toUpperCase();
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
        if (log.response_status) {
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
