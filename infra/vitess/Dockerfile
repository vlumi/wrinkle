# vttestserver for arm64 (and amd64), built from the Vitess sources at a
# release tag. Vitess publishes amd64 images only; this mirrors the official
# docker/vttestserver image, with MySQL coming from the multi-arch mysql
# image because there are no arm64 MySQL packages for Debian.

ARG VITESS_VERSION=v24.0.4
ARG MYSQL_VERSION=8.4.9

FROM golang:1.26-bookworm AS builder
ARG VITESS_VERSION
WORKDIR /vt/src/vitess.io/vitess
RUN git clone --depth 1 --branch "${VITESS_VERSION}" https://github.com/vitessio/vitess.git . \
    && mkdir -p /vt/install/bin \
    && CGO_ENABLED=0 go build -trimpath \
        -ldflags "$(tools/build_version_flags.sh)" \
        -o /vt/install/bin ./go/cmd/mysqlctl ./go/cmd/mysqlctld ./go/cmd/vtcombo ./go/cmd/vttestserver \
    && cp -R config /vt/install/

FROM mysql:${MYSQL_VERSION}
ARG MYSQL_VERSION
USER root
RUN microdnf install -y procps-ng && microdnf clean all \
    && groupadd -r vitess && useradd -r -g vitess vitess \
    && mkdir -p /vt/vtdataroot && chown -R vitess:vitess /vt
ENV VTROOT=/vt
ENV VTDATAROOT=/vt/vtdataroot
ENV PATH=/vt/bin:$PATH
ENV MYSQL_SERVER_VERSION=${MYSQL_VERSION}-Vitess
COPY --from=builder --chown=vitess:vitess /vt/install /vt
COPY --chown=vitess:vitess run.sh /vt/run.sh
VOLUME /vt/vtdataroot
USER vitess
ENTRYPOINT ["/vt/run.sh"]
