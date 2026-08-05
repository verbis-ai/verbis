

# Verbis AI

[![Discord](https://dcbadge.vercel.app/api/server/GDdQ7D3J2E?style=flat&compact=true)](https://discord.gg/GDdQ7D3J2E)

Verbis AI es un asistente de IA seguro y totalmente local para MacOS. Al conectarse a tus diversas aplicaciones SaaS, Verbis AI indexa todos tus datos de forma segura y local en tu sistema. Verbis proporciona una interfaz única para consultar y gestionar tu información con el poder de los modelos de GenAI.

### MacOS
[Descargar](https://verbis-releases.s3.amazonaws.com/v0.0.3/Verbis.dmg)

### Inicio rápido

1. [Descargar](https://verbis-releases.s3.amazonaws.com/v0.0.3/Verbis.dmg) e instalar Verbis
2. Conecta Verbis a tus fuentes de datos (Google Drive, Outlook, Gmail, Slack, etc.)
3. Usa Verbis como un chatbot para buscar en tus datos. Tus datos nunca salen de tu dispositivo.

### Video de demostración
[![Demo de Verbis AI](http://img.youtube.com/vi/TRmKgoDQy7A/0.jpg)](https://youtu.be/TRmKgoDQy7A "Demo de Verbis AI")

### Configuración inicial
Verbis descarga e indexa localmente documentos de servicios de terceros autenticados mediante OAuth, denominados “apps” (aplicaciones). Para gestionar tus apps:

- Haz clic en el icono de engranaje en la esquina superior derecha de la ventana de Verbis.
- Aparecerá una lista de apps, junto con información sobre los documentos sincronizados.
- Para agregar una nueva app, selecciona la app del catálogo de aplicaciones y haz clic en el botón “Connect” (Conectar).
- Tu última ventana de navegador activa debería redirigirte a una pantalla de consentimiento de OAuth.
- Después de completar el flujo de consentimiento de OAuth, la aplicación comenzará a sincronizar documentos localmente de forma automática.
- Si una aplicación no es compatible, puedes hacer clic en el botón “Request” (Solicitar) para notificar a nuestro equipo sobre tu solicitud de soporte futuro.

## Detalles técnicos
Verbis AI se ejecuta con Ollama y Weaviate, y utilizamos los siguientes modelos: `Mistral 7B`, `ms-marco-MiniLM-L-12-v2` y `nomic-embed-text`.

### Requisitos del sistema
- Mac con Apple Silicon (m1+): Macbook, Mac mini, Mac Pro, Mac Studio

#### Consumo esperado de recursos del sistema
- Disco: 6 GB para los pesos del modelo, aproximadamente 1-4 GB dependiendo de la configuración del conector y los datos sincronizados.
- Todos los datos se almacenan en ~/.verbis
- Memoria: Aproximadamente 1.2 GB para los modelos y de 200 MB a 2 GB para los índices
- Los modelos se descargan de la memoria después de 20 minutos de inactividad
- Procesamiento (Compute): Depende del chipset. Requisitos muy bajos de CPU durante la sincronización, picos abruptos de uso de GPU durante la inferencia durante 1-8 segundos 
- Red: Se pueden descargar hasta 10 documentos simultáneamente desde cada conector a ancho de banda de red máximo durante la sincronización

### Información de contacto
El equipo de Verbis AI (info@verbis.ai)

- Sahil Kumar (sahil@verbis.ai)
- Alex Mavrogiannis (alex@verbis.ai)

### Comunicaciones con terceros
Verbis recibe datos de apps SaaS y envía datos de telemetría a Posthog. Tus datos nunca salen de tu sistema. La telemetría se puede desactivar desde la página de configuración. Nuestra política de privacidad completa está disponible [aquí](https://www.verbis.ai/privacy-policy)

#### Datos de aplicaciones SaaS (“connectors”)
Se descargan en el host local que ejecuta Verbis AI utilizando credenciales de OAuth y nunca se comparten con otros terceros.

#### Almacenamiento de pesos de modelos
Los pesos de los modelos para los siguientes modelos se obtienen de la Biblioteca de Ollama y Huggingface durante la inicialización:

- Mistral 7B v0.3

#### Telemetría
La telemetría es una función con opción de exclusión (opt-out), pero animamos a los usuarios a mantenerla activada para ayudar al equipo a mejorar Verbis. Cuando la telemetría está activada, los siguientes eventos se reportarán a eu.posthog.com mediante una llamada HTTP POST:

- Aplicación iniciada
    - Chipset
    - Versión de MacOS
    - Tamaño de memoria
    - Tiempo de arranque
    - Dirección IP
- Sincronización del conector completada 
    - ID del conector
    - Tipo de conector
    - Número de documentos sincronizados
    - Número de chunks sincronizados
    - Número de errores
    - Mensaje de error de sincronización
    - Duración de la sincronización
    - Dirección IP
- Prompt
    - Duración de cada fase de procesamiento del prompt
    - Número de resultados de búsqueda
    - Número de resultados reranked

### Desarrollo

Para desarrollar y compilar verbis, se necesitan las siguientes herramientas en tu máquina local:
- Go 1.22 o posterior (`brew install go`)
- Python y utilidades (`make builder-env`)
- NVM con node v21.6.2 o posterior
- Una copia de `.build.env` que contenga claves API y otras variables requeridas para el proceso de compilación
- Una copia de `dist/credentials.json`, utilizada para las credenciales de OAuth de Google
