# Verbis AI

[![Discord](https://dcbadge.vercel.app/api/server/GDdQ7D3J2E?style=flat&compact=true)](https://discord.gg/GDdQ7D3J2E)

Verbis AI is a secure and fully local AI assistant for MacOS. By connecting to your
various SaaS applications, Verbis AI indexes all your data securely and locally
on your system. Verbis provides a single interface to query and manage your
information with the power of GenAI models.

### MacOS
[Download](https://verbis-releases.s3.amazonaws.com/v0.0.3/Verbis.dmg)

### Quickstart

1. [Download](https://verbis-releases.s3.amazonaws.com/v0.0.3/Verbis.dmg) and 
install Verbis
2. Connect Verbis to your data sources (Google Drive, Outlook, Gmail, Slack etc)
3. Use Verbis as a chatbot to search across your data. Your data never leaves
   your device.

### Demo Video
[![Verbis AI Demo](http://img.youtube.com/vi/TRmKgoDQy7A/0.jpg)](https://youtu.be/TRmKgoDQy7A "Verbis AI Demo")

### Initial Configuration
Verbis downloads and locally indexes documents from third-party services
authenticated via OAuth, called “apps”. To manage your apps:

- Click the gear icon on the top right of the Verbis window.
- A list of apps will appear, along with information on synchronized
documents.
- To add a new app, select the app from the app catalog and click the “Connect”
button.
- Your last active browser window should navigate to an OAuth consent screen.
- After completing the OAuth consent flow, the application will automatically
begin syncing documents locally.
- If an application is not supported, you may click the “Request” button to notify
our team of your request for future support.

## Technical details
Verbis AI is powered by Ollama and Weaviate, and we use the following models:
`Mistral 7B`, `ms-marco-MiniLM-L-12-v2`, and `nomic-embed-text`.

### System Requirements
- Apple Silicon Mac (m1+): Macbook, Mac mini, Mac Pro, Mac Studio

#### Expected system resource utilization
- Disk: 6 GB for model weights, approximately 1-4 GB depending on connector configuration and synced data.
- All data is stored under ~/.verbis
- Memory: Approximately 1.2 GB for models and 200MB to 2 GB for indexes
- Models are unloaded from memory after 20 minutes of inactivity
- Compute: Depends on chipset. Very low CPU requirements during syncing, sharp spikes in GPU utilization during inference for 1-8 seconds 
- Network: Up to 10 documents may be downloaded concurrently from each connector at peak network bandwidth during syncing

### Contact Information
The Verbis AI team (info@verbis.ai)

- Sahil Kumar (sahil@verbis.ai)
- Alex Mavrogiannis (alex@verbis.ai)

### Communications with third parties
Verbis receives data from SaaS apps, sends telemetry data to Posthog. Your data
never leaves your system. Telemetry can be disabled via the settings page. Our
full privacy policy is available [here](https://www.verbis.ai/privacy-policy)

#### SaaS application data (“connectors”)
Downloaded to the local host running Verbis AI using OAuth credentials, and never shared with other third parties

#### Model weight storage
Model weights for the following models are fetched from either the Ollama Library and Huggingface during initialization:

- Mistral 7B v0.3

#### Telemetry
Telemetry is an opt-out feature, but we encourage users to keep telemetry
enabled to help the team improve Verbis. When telemetry is enabled, the
following events will be reported to eu.posthog.com via an HTTP POST call:

- Application started
    - Chipset
    - MacOS version
    - memory size
    - Time to boot
    - IP Address
- Connector sync complete 
    - Connector ID
    - Connector type
    - Number of synced documents
    - Number of synced chunks
    - Number of errors
    - Sync error message
    - Sync duration
    - IP Address
- Prompt
    - Duration of each prompt processing phase
    - Number of search results
    - Number of reranked results

### Development

To develop and build verbis, the following tools are needed on your local machine:
- Go 1.22 or later (`brew install go`)
- Python & utilities (`make builder-env`)
- NVM with node v21.6.2 or later
- A copy of `.build.env` containing API keys and other variables required for the build process
- A copy of `dist/credentials.json`, used for Google OAuth credentials 
