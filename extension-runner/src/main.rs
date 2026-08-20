mod framing;

use anyhow::{Context, Result, anyhow, bail};
use cap_std::{ambient_authority, fs::Dir};
use framing::{read_frame, write_frame};
use prost::Message;
use sha2::{Digest, Sha256};
use std::collections::{HashMap, HashSet};
use std::io::{self, Read};
use std::net::{IpAddr, ToSocketAddrs};
use std::thread;
use std::time::Duration;
use url::Url;
use wasmtime::component::{Component, Linker};
use wasmtime::{Config, Engine, Store, StoreLimits, StoreLimitsBuilder};

mod protocol {
    include!(concat!(env!("OUT_DIR"), "/gugumanager.extension.runner.v1.rs"));
}

wasmtime::component::bindgen!({
    path: "wit",
    world: "extension",
});

const RUNNER_VERSION: &str = env!("CARGO_PKG_VERSION");

struct RunnerState {
    root: Option<Dir>,
    permissions: HashSet<String>,
    allowlist: HashSet<String>,
    secrets: HashMap<String, Vec<u8>>,
    progress: Vec<(u8, String)>,
    limits: StoreLimits,
    max_http_bytes: usize,
}

impl gugumanager::extension::host::Host for RunnerState {
    fn progress(&mut self, percent: u8, message: String) {
        self.progress.push((percent.min(100), truncate(message, 1024)));
    }

    fn read_file(&mut self, path: String) -> Result<Result<Vec<u8>, String>> {
        if !self.permissions.contains("server-data-read") {
            return Ok(Err("permission denied".into()));
        }
        let path = safe_relative_path(&path).map_err(|error| error.to_string())?;
        let root = self.root.as_ref().ok_or_else(|| anyhow!("server root unavailable"))?;
        let mut file = match root.open(path) {
            Ok(file) => file,
            Err(error) => return Ok(Err(error.to_string())),
        };
        let mut content = Vec::new();
        file.take(8 * 1024 * 1024 + 1).read_to_end(&mut content)?;
        if content.len() > 8 * 1024 * 1024 {
            return Ok(Err("file exceeds host read limit".into()));
        }
        Ok(Ok(content))
    }

    fn write_file(&mut self, path: String, content: Vec<u8>) -> Result<Result<(), String>> {
        if !self.permissions.contains("server-data-write") {
            return Ok(Err("permission denied".into()));
        }
        if content.len() > 8 * 1024 * 1024 {
            return Ok(Err("file exceeds host write limit".into()));
        }
        let path = safe_relative_path(&path).map_err(|error| error.to_string())?;
        let root = self.root.as_ref().ok_or_else(|| anyhow!("server root unavailable"))?;
        match root.write(path, content) {
            Ok(()) => Ok(Ok(())),
            Err(error) => Ok(Err(error.to_string())),
        }
    }

    fn https_get(&mut self, url: String, secret_binding: Option<String>) -> Result<Result<gugumanager::extension::host::HttpResponse, String>> {
        if !self.permissions.contains("https") {
            return Ok(Err("permission denied".into()));
        }
        let parsed = match validate_https_target(&url, &self.allowlist) {
            Ok(parsed) => parsed,
            Err(error) => return Ok(Err(error.to_string())),
        };
        let mut request = ureq::get(parsed.as_str());
        if let Some(binding) = secret_binding {
            if !self.permissions.contains("secret-bind") {
                return Ok(Err("secret binding permission denied".into()));
            }
            let secret = match self.secrets.get(&binding) {
                Some(value) => value,
                None => return Ok(Err("unknown secret binding".into())),
            };
            let value = match std::str::from_utf8(secret) {
                Ok(value) => value,
                Err(_) => return Ok(Err("secret binding is not valid UTF-8".into())),
            };
            request = request.header("authorization", value);
        }
        let mut response = match request.config().max_redirects(0).build().call() {
            Ok(response) => response,
            Err(error) => return Ok(Err(error.to_string())),
        };
        let status = response.status().as_u16();
        let body = match response.body_mut().with_config().limit(self.max_http_bytes as u64 + 1).read_to_vec() {
            Ok(body) if body.len() <= self.max_http_bytes => body,
            Ok(_) => return Ok(Err("HTTP response exceeds byte limit".into())),
            Err(error) => return Ok(Err(error.to_string())),
        };
        Ok(Ok(gugumanager::extension::host::HttpResponse { status, body }))
    }
}

fn main() -> Result<()> {
    let mut input = io::stdin().lock();
    let mut output = io::stdout().lock();
    while let Some(frame) = read_frame(&mut input)? {
        let request = protocol::Request::decode(frame.as_slice()).context("decode request")?;
        let responses = handle(request);
        for response in responses {
            let encoded = response.encode_to_vec();
            write_frame(&mut output, &encoded)?;
        }
    }
    Ok(())
}

fn handle(request: protocol::Request) -> Vec<protocol::Response> {
    use protocol::request::Payload;
    let request_id = request.request_id;
    match request.payload {
        Some(Payload::Hello(hello)) => {
            let version = if hello.protocol_versions.contains(&1) { 1 } else { 0 };
            vec![protocol::Response {
                request_id,
                payload: Some(protocol::response::Payload::Hello(protocol::HelloAck {
                    protocol_version: version,
                    runner_version: RUNNER_VERSION.into(),
                    capabilities: vec!["wasi-component/v1".into(), "framing/protobuf-v1".into()],
                })),
            }]
        }
        Some(Payload::Invoke(invoke)) => invoke_component(request_id, invoke),
        Some(Payload::Cancel(_)) | None => vec![result_response(request_id, false, "INVALID_REQUEST", vec![], "cancel requires an active async invocation")],
    }
}

fn invoke_component(request_id: String, invoke: protocol::Invoke) -> Vec<protocol::Response> {
    match run_component(&invoke) {
        Ok((output, progress)) => {
            let mut responses = progress.into_iter().map(|(percent, message)| protocol::Response {
                request_id: request_id.clone(),
                payload: Some(protocol::response::Payload::Progress(protocol::Progress { percent: percent.into(), message })),
            }).collect::<Vec<_>>();
            responses.push(result_response(request_id, true, "", output, ""));
            responses
        }
        Err(error) => vec![result_response(request_id, false, "EXTENSION_FAILED", vec![], &truncate(format!("{error:#}"), 4096))],
    }
}

fn run_component(invoke: &protocol::Invoke) -> Result<(Vec<u8>, Vec<(u8, String)>)> {
    validate_invoke(invoke)?;
    let mut config = Config::new();
    config.wasm_component_model(true).consume_fuel(true).epoch_interruption(true);
    let engine = Engine::new(&config)?;
    let component = Component::new(&engine, &invoke.component)?;
    let memory_bytes = invoke.memory_bytes.clamp(16 * 1024 * 1024, 512 * 1024 * 1024) as usize;
    let limits = StoreLimitsBuilder::new().memory_size(memory_bytes).instances(2).tables(8).build();
    let root = if invoke.permissions.iter().any(|permission| permission.starts_with("server-data-")) {
        Some(Dir::open_ambient_dir(&invoke.server_root, ambient_authority())?)
    } else {
        None
    };
    let state = RunnerState {
        root,
        permissions: invoke.permissions.iter().cloned().collect(),
        allowlist: invoke.https_allowlist.iter().map(|host| host.to_ascii_lowercase()).collect(),
        secrets: invoke.secret_bindings.clone(),
        progress: Vec::new(),
        limits,
        max_http_bytes: 8 * 1024 * 1024,
    };
    let mut store = Store::new(&engine, state);
    store.limiter(|state| &mut state.limits);
    store.set_fuel(invoke.fuel.clamp(10_000, 10_000_000_000))?;
    store.set_epoch_deadline(1);
    let timer_engine = engine.clone();
    let timeout = Duration::from_millis(u64::from(invoke.timeout_ms.clamp(100, 120_000)));
    thread::spawn(move || {
        thread::sleep(timeout);
        timer_engine.increment_epoch();
    });
    let mut linker = Linker::new(&engine);
    Extension::add_to_linker(&mut linker, |state| state)?;
    let bindings = Extension::instantiate(&mut store, &component, &linker)?;
    let result = bindings.call_run(&mut store, &invoke.input)?;
    let output = result.map_err(|error| anyhow!(error))?;
    let max_output = if invoke.max_output_bytes == 0 { 1024 * 1024 } else { invoke.max_output_bytes.min(16 * 1024 * 1024) };
    if output.len() as u64 > max_output {
        bail!("HOOK_OUTPUT_LIMIT");
    }
    Ok((output, std::mem::take(&mut store.data_mut().progress)))
}

fn validate_invoke(invoke: &protocol::Invoke) -> Result<()> {
    if invoke.operation_id.is_empty() || invoke.server_id.is_empty() || invoke.component.is_empty() {
        bail!("missing invocation identity or component");
    }
    for (label, value) in [("bundle", &invoke.bundle_digest), ("extension", &invoke.extension_digest)] {
        let actual = format!("sha256:{:x}", Sha256::digest(if label == "extension" { &invoke.component } else { &[] }));
        if !value.starts_with("sha256:") || value.len() != 71 {
            bail!("invalid {label} digest");
        }
        if label == "extension" && value != &actual {
            bail!("extension digest mismatch");
        }
    }
    Ok(())
}

fn safe_relative_path(path: &str) -> Result<&str> {
    if path.is_empty() || path.contains('\\') || path.starts_with('/') || path.split('/').any(|part| part.is_empty() || part == "." || part == "..") {
        bail!("unsafe relative path");
    }
    Ok(path)
}

fn validate_https_target(raw: &str, allowlist: &HashSet<String>) -> Result<Url> {
    let parsed = Url::parse(raw)?;
    if parsed.scheme() != "https" || parsed.username() != "" || parsed.password().is_some() || parsed.port().is_some() {
        bail!("only canonical HTTPS URLs are allowed");
    }
    let host = parsed.host_str().ok_or_else(|| anyhow!("URL has no host"))?.to_ascii_lowercase();
    if !allowlist.contains(&host) {
        bail!("host is not allowlisted");
    }
    let addresses = (host.as_str(), 443).to_socket_addrs()?;
    for address in addresses {
        if !is_public_ip(address.ip()) {
            bail!("host resolves to a private or special-use address");
        }
    }
    Ok(parsed)
}

fn is_public_ip(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(ip) => !(ip.is_private() || ip.is_loopback() || ip.is_link_local() || ip.is_broadcast() || ip.is_documentation() || ip.is_unspecified()),
        IpAddr::V6(ip) => !(ip.is_loopback() || ip.is_unspecified() || ip.is_unique_local() || ip.is_unicast_link_local()),
    }
}

fn truncate(mut value: String, max: usize) -> String {
    if value.len() > max {
        value.truncate(max);
    }
    value
}

fn result_response(request_id: String, succeeded: bool, error_code: &str, output: Vec<u8>, detail: &str) -> protocol::Response {
    protocol::Response {
        request_id,
        payload: Some(protocol::response::Payload::Result(protocol::Result {
            succeeded,
            error_code: error_code.into(),
            output,
            detail: detail.into(),
        })),
    }
}
