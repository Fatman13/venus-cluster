//! config for venus-worker

use std::collections::HashMap;
use std::fs::File;
use std::io::Read;
use std::net::SocketAddr;
use std::path::Path;
use std::time::Duration;

use anyhow::{ensure, Result};
use jsonrpc_core_client::transports::ws::ConnectInfo;
use serde::{Deserialize, Serialize};
use toml::from_slice;

use crate::sealing::processor::external::config::Ext;

pub const DEFAULT_WORKER_SERVER_PORT: u16 = 17890;
pub const DEFAULT_WORKER_SERVER_HOST: &str = "0.0.0.0";

/// configurations for sealing sectors
#[derive(Debug, Clone)]
pub struct Sealing {
    /// specified miner actors
    pub allowed_miners: Option<Vec<u64>>,

    /// specified sector sizes
    pub allowed_sizes: Option<Vec<String>>,

    /// enable sealing sectors with deal pieces
    pub enable_deals: bool,

    /// max retry times for tempoary failed sector
    pub max_retries: u32,

    /// interval between sectors
    pub seal_interval: Duration,

    /// interval between retry attempts
    pub recover_interval: Duration,

    /// interval between polling requests
    pub rpc_polling_interval: Duration,

    /// ignore proof state check
    pub ignore_proof_check: bool,
}

impl Default for Sealing {
    fn default() -> Self {
        Sealing {
            allowed_miners: None,
            allowed_sizes: None,
            enable_deals: false,
            max_retries: 5,
            seal_interval: Duration::from_secs(30),
            recover_interval: Duration::from_secs(30),
            rpc_polling_interval: Duration::from_secs(30),
            ignore_proof_check: false,
        }
    }
}

/// configurations for sealing sectors
#[derive(Debug, Default, Serialize, Deserialize)]
pub struct SealingOptional {
    /// specified miner actors
    pub allowed_miners: Option<Vec<u64>>,

    /// specified sector sizes
    pub allowed_sizes: Option<Vec<String>>,

    /// enable sealing sectors with deal pieces
    pub enable_deals: Option<bool>,

    /// max retry times for tempoary failed sector
    pub max_retries: Option<u32>,

    /// interval between sectors
    #[serde(default)]
    #[serde(with = "humantime_serde")]
    pub seal_interval: Option<Duration>,

    /// interval between retry attempts
    #[serde(default)]
    #[serde(with = "humantime_serde")]
    pub recover_interval: Option<Duration>,

    /// interval between polling requests
    #[serde(default)]
    #[serde(with = "humantime_serde")]
    pub rpc_polling_interval: Option<Duration>,

    /// ignore proof state check
    pub ignore_proof_check: Option<bool>,
}

impl SealingOptional {
    fn check(&self) -> Result<()> {
        for (name, interval) in &[
            ("seal_interval", self.seal_interval),
            ("recover_interval", self.recover_interval),
            ("rpc_polling_interval", self.rpc_polling_interval),
        ] {
            ensure!(
                interval.map(|v| v.as_secs() > 0).unwrap_or(true),
                format!("{} should be greater than '0's", name)
            );
        }

        ensure!(
            self.max_retries.map(|re| re <= 1024).unwrap_or(true),
            "max_retries should be less or equal than 1024"
        );

        Ok(())
    }
}

/// configuration for remote store
#[derive(Debug, Default, Serialize, Deserialize)]
pub struct Remote {
    /// store path, if we are using fs based store
    pub path: Option<String>,
    pub instance: Option<String>,
}

/// configurations for sector store
#[derive(Debug, Default, Serialize, Deserialize)]
pub struct Store {
    /// store location
    pub location: String,

    /// special sealing configuration
    pub sealing: Option<SealingOptional>,
}

/// configurations for rpc
#[derive(Debug, Default, Serialize, Deserialize)]
pub struct RPCClient {
    /// jsonrpc endpoint
    pub url: String,
    pub headers: Option<HashMap<String, String>>,
}

impl RPCClient {
    pub fn to_connect_info(&self) -> ConnectInfo {
        ConnectInfo {
            url: self.url.clone(),
            headers: self.headers.as_ref().cloned().unwrap_or_default(),
        }
    }
}

#[derive(Debug, Default, Serialize, Deserialize)]
pub struct RPCServer {
    /// jsonrpc endpoint
    pub host: Option<String>,
    pub port: Option<u16>,
}

/// configurations for processors
#[derive(Debug, Default, Serialize, Deserialize)]
pub struct Processors {
    /// section for pc2 processor
    pub pc2: Option<Ext>,

    /// section for c2 processor
    pub c2: Option<Ext>,
}

#[derive(Debug, Default, Serialize, Deserialize)]
pub struct InstanceConfig {
    pub name: Option<String>,
}

/// global configuration
#[derive(Debug, Default, Serialize, Deserialize)]
pub struct Config {
    /// section for local config
    pub instance: Option<InstanceConfig>,

    /// section for worker server
    pub worker_server: Option<RPCServer>,

    /// section for rpc
    pub sealer_rpc: RPCClient,

    /// section for common sealing
    pub sealing: SealingOptional,

    /// section for list of sector stores
    pub stores: Vec<Store>,

    /// section for concurrent limit
    pub limit: HashMap<String, usize>,

    /// section for remote store
    pub remote: Remote,

    /// section for processors
    pub processors: Option<Processors>,
}

impl Config {
    /// load config from the reader
    pub fn from_reader<R: Read>(mut r: R) -> Result<Self> {
        let mut content = Vec::with_capacity(1 << 10);
        r.read_to_end(&mut content)?;

        let cfg: Self = from_slice(&content)?;
        cfg.check()?;

        Ok(cfg)
    }

    /// load from config file
    pub fn load<P: AsRef<Path>>(p: P) -> Result<Self> {
        let f = File::open(p)?;
        let cfg = Self::from_reader(f)?;
        cfg.check()?;
        Ok(cfg)
    }

    fn check(&self) -> Result<()> {
        for store in &self.stores {
            if let Some(cfg) = &store.sealing {
                cfg.check()?;
            }
        }

        self.sealing.check()
    }
}

impl Config {
    /// get listen addr for worker server
    pub fn worker_server_listen_addr(&self) -> Result<SocketAddr> {
        let host = self
            .worker_server
            .as_ref()
            .and_then(|c| c.host.as_ref())
            .map(|s| s.as_str())
            .unwrap_or(DEFAULT_WORKER_SERVER_HOST);

        let port = self
            .worker_server
            .as_ref()
            .and_then(|c| c.port.as_ref())
            .cloned()
            .unwrap_or(DEFAULT_WORKER_SERVER_PORT);

        let addr = format!("{}:{}", host, port).parse()?;
        Ok(addr)
    }
}
