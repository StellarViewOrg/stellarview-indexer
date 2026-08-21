FROM rust:1.85-bookworm

ARG STELLAR_CLI_VERSION=22.0.8
ENV RUST_VERSION=1.85.0 \
    STELLAR_CLI_VERSION=${STELLAR_CLI_VERSION} \
    HOME=/usr/local/cargo \
    CARGO_HOME=/usr/local/cargo

RUN rustup target add wasm32-unknown-unknown

RUN cargo install --locked stellar-cli@${STELLAR_CLI_VERSION} \
    && mv /usr/local/cargo/bin/stellar /usr/local/bin/stellar \
    && chown -R 1000:1000 /usr/local/cargo

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh \
    && mkdir -p /src /out /build \
    && chown -R 1000:1000 /out

VOLUME ["/usr/local/cargo"]

USER 1000:1000
WORKDIR /src

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
