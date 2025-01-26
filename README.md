# backstream

Expose your local applications to the internet with self-hosted reverse proxy.

![overview](doc/images/overview.jpg)

The backstream provides a simple way to expose your local applications to the internet. It is a self-hosted reverse proxy that allows you to access your local applications from anywhere. It is useful for testing webhooks, APIs, and web applications on your local machine.

## Motivation

This feature is like [ngrok](https://ngrok.com/), [localtunnel](https://localtunnel.github.io/www/), and [serveo](https://serveo.net/). However, backstream is a self-hosted reverse proxy that you can run on your own server. It is open-source and free to use.

- **Security**: Your data never leaves your server. You don't have to trust third-party services. You can handle any sensitive data (Credential, Personal information) on your server.
- **Custom Domain**: You can use your own domain name. You can set up a subdomain like `app.example.com` to access your local application.

## License

Apache License 2.0
