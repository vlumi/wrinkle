# Standalone HBase: the master, one region server and ZooKeeper in one JVM,
# storing on the local filesystem, with no HDFS. Apache publishes no image,
# so this installs the release binaries on a JDK the release supports.

ARG HBASE_VERSION=2.6.7

FROM eclipse-temurin:17-jre
ARG HBASE_VERSION
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ARG HBASE_SHA512=2e0835a3ed8e5a9114f5d878deafb2f7a6b1c8386ba422d1a307fcc5551d452dc7da468729a43acf75dcfe0b20424cb48f72ed424dfd15f81b1b3624454ed82d
RUN wget -q -O /tmp/hbase.tar.gz \
        "https://archive.apache.org/dist/hbase/${HBASE_VERSION}/hbase-${HBASE_VERSION}-hadoop3-bin.tar.gz" \
    && echo "${HBASE_SHA512}  /tmp/hbase.tar.gz" | sha512sum -c - \
    && mkdir /opt/hbase \
    && tar -xzf /tmp/hbase.tar.gz -C /opt/hbase --strip-components 1 \
        --exclude 'docs' --exclude '*-tests.jar' \
    && rm /tmp/hbase.tar.gz \
    && groupadd -r hbase && useradd -r -g hbase -d /opt/hbase hbase \
    && mkdir -p /data /opt/hbase/logs && chown hbase:hbase /data /opt/hbase/logs
ENV PATH=/opt/hbase/bin:$PATH
ENV HBASE_MANAGES_ZK=true
COPY hbase-site.xml /opt/hbase/conf/hbase-site.xml
USER hbase
EXPOSE 2181 16000 16010 16020 16030
CMD ["hbase", "master", "start"]
