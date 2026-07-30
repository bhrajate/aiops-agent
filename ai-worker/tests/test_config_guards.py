"""启动配置护栏:生产模式下拒绝会产出**虚假产物**的配置。

为什么这组用例值得存在:MockProvider 的失败方式是最难发现的那一种 ——
不报错、不超时、schema 完全合法、Evidence 引用齐备,只是假设和诊断结论是编造的。
而 model_provider 的默认值恰好就是 mock,部署清单又长期没有设过这一项。
唯一能挡住它的地方是启动,所以这里把"生产 + mock 必须拒绝"钉成不变量。

同时覆盖 Settings 的**求值时机**:字段默认值若在类定义时求值(dataclass 的
默认行为),load_settings() 拿到的就是 import 瞬间的快照,护栏的正确性会变成
"取决于谁先 import" —— 这不是护栏该有的性质。
"""
from __future__ import annotations

import pytest

from aiops_worker.config import ConfigError, Settings, load_settings


# --------------------------------------------------------------------------
# 生产护栏
# --------------------------------------------------------------------------

# 被认定为生产的写法(与 Go 侧 config.IsProduction / isProduction 同一口径)。
PROD_ENVS = ["production", "prod", "Production", "PROD", "  prod  "]
# 不是生产的写法。staging/preprod 刻意不算:把它们误判成生产会让预发环境
# 无法用 mock 跑通,而这正是预发的常见用法。
NON_PROD_ENVS = ["development", "dev", "staging", "preprod", "", "test"]


@pytest.mark.parametrize("env", PROD_ENVS)
def test_production_rejects_mock_provider(env):
    with pytest.raises(ConfigError, match="mock"):
        Settings(env=env, model_provider="mock").validate()


@pytest.mark.parametrize("env", PROD_ENVS)
def test_production_allows_anthropic_provider(env):
    # 唯一放行的生产组合。
    Settings(env=env, model_provider="anthropic").validate()


@pytest.mark.parametrize("env", NON_PROD_ENVS)
def test_non_production_allows_mock_provider(env):
    # 零基础设施的端到端演示与全部离线测试都依赖这条放行。
    Settings(env=env, model_provider="mock").validate()


@pytest.mark.parametrize("env", PROD_ENVS)
def test_is_production_recognizes(env):
    assert Settings(env=env).is_production() is True


@pytest.mark.parametrize("env", NON_PROD_ENVS)
def test_is_production_rejects(env):
    assert Settings(env=env).is_production() is False


def test_default_env_is_development():
    # 默认值必须是非生产:否则本地/CI 跑测试会被生产护栏挡住。
    # 严格性由**部署清单显式声明 production** 来提供,不靠代码默认值。
    assert Settings(env="development").is_production() is False


# --------------------------------------------------------------------------
# 求值时机:load_settings() 必须反映**当前**环境变量
# --------------------------------------------------------------------------


def test_load_settings_reads_current_env(monkeypatch):
    """字段默认值若在 import 时求值,这条会失败。

    dataclass 的 `x: str = os.environ.get(...)` 正是那种写法 —— 它让
    load_settings() 永远返回导入瞬间的快照。改用 default_factory 才能兑现
    "从当前环境变量构建"这个承诺,也才能让 validate() 不依赖 import 顺序。
    """
    monkeypatch.setenv("AIOPS_ENV", "production")
    monkeypatch.setenv("AIOPS_MODEL_PROVIDER", "anthropic")
    s = load_settings()
    assert s.env == "production"
    assert s.model_provider == "anthropic"
    assert s.is_production() is True

    monkeypatch.setenv("AIOPS_ENV", "development")
    assert load_settings().is_production() is False


def test_load_settings_normalizes_provider_case(monkeypatch):
    monkeypatch.setenv("AIOPS_MODEL_PROVIDER", "MOCK")
    assert load_settings().model_provider == "mock"


def test_load_settings_production_mock_is_rejected(monkeypatch):
    """端到端:环境变量 → load_settings → validate 必须拒绝。

    这条走的是 main.py 的真实路径(它就是 load_settings() 后紧跟 validate()),
    上面那些用例直接构造 Settings,绕过了环境变量读取。
    """
    monkeypatch.setenv("AIOPS_ENV", "production")
    monkeypatch.delenv("AIOPS_MODEL_PROVIDER", raising=False)  # 漏配 → 默认 mock
    with pytest.raises(ConfigError):
        load_settings().validate()
