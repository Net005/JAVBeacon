using Jellyfin.Plugin.JAVBeacon.Configuration;
using MediaBrowser.Common.Configuration;
using MediaBrowser.Common.Plugins;
using MediaBrowser.Model.Plugins;
using MediaBrowser.Model.Serialization;

namespace Jellyfin.Plugin.JAVBeacon;

public sealed class Plugin : BasePlugin<PluginConfiguration>, IHasWebPages
{
    public Plugin(IApplicationPaths paths, IXmlSerializer serializer) : base(paths, serializer) => Instance = this;
    public static Plugin? Instance { get; private set; }
    public override string Name => "JAVBeacon";
    public override string Description => "JAVBeacon metadata and Stash activity bridge.";
    public override Guid Id => Guid.Parse("36dc106a-df2f-4d33-bb71-574aa09e6d5b");

    public IEnumerable<PluginPageInfo> GetPages() => [new()
    {
        Name = "javbeacon",
        EmbeddedResourcePath = $"{GetType().Namespace}.Configuration.config.html"
    }];

}
